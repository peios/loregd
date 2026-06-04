package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/peios/loregd/internal/hivedb"
)

// errReadOnlyTxn is returned when a mutating operation is tagged with a
// read-only transaction ID. Per PSD-005 §7.2, loregd MUST reject it with
// RSI_INVALID and MUST NOT mutate state.
var errReadOnlyTxn = errors.New("mutating operation on read-only transaction")

// txnInfo tracks state for one active RSI transaction.
type txnInfo struct {
	hive    *hivedb.HiveDB // nil = unbound (no operation has bound a connection yet)
	conn    *sql.Conn      // pinned connection (write conn for RW, snapshot conn for RO)
	querier *connQuerier   // wraps conn for Querier interface
	binding bool           // true while bind is in progress (prevents double-bind)

	readOnly   bool    // RSI_TXN_READ_ONLY: snapshot reads, mutations rejected
	snapshotDB *sql.DB // dedicated DB backing a read-only snapshot conn (nil for RW)

	// Deferred actions for cache consistency.
	onCommit []func() // run after successful COMMIT
	onAbort  []func() // run after ROLLBACK or cleanup
}

// txnManager tracks all active RSI transactions.
type txnManager struct {
	mu   sync.Mutex
	txns map[uint64]*txnInfo
}

func newTxnManager() *txnManager {
	return &txnManager{txns: make(map[uint64]*txnInfo)}
}

// begin registers a new pending read-write transaction (unbound).
func (m *txnManager) begin(txnID uint64) error {
	return m.beginMode(txnID, false)
}

// beginMode registers a new pending transaction (unbound). readOnly selects
// RSI_TXN_READ_ONLY (snapshot) semantics. Returns an error if txnID is
// already registered.
func (m *txnManager) beginMode(txnID uint64, readOnly bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.txns[txnID]; exists {
		return fmt.Errorf("transaction %d already active", txnID)
	}
	m.txns[txnID] = &txnInfo{readOnly: readOnly}
	return nil
}

// getOrBindWrite returns the transaction's write querier, binding it to the
// hive on the first mutating operation. Returns (querier, error). If
// SQLITE_BUSY, the error is detectable via isBusy(). If the transaction is
// read-only, returns errReadOnlyTxn without binding or mutating.
func (m *txnManager) getOrBindWrite(txnID uint64, hive *hivedb.HiveDB) (Querier, error) {
	m.mu.Lock()
	info := m.txns[txnID]
	if info == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("unknown transaction %d", txnID)
	}
	if info.readOnly {
		m.mu.Unlock()
		return nil, errReadOnlyTxn
	}

	// Already bound — verify same hive and return existing querier.
	if info.conn != nil {
		if info.hive != hive {
			m.mu.Unlock()
			return nil, fmt.Errorf("transaction %d bound to different hive", txnID)
		}
		q := info.querier
		m.mu.Unlock()
		return q, nil
	}

	// Prevent double-bind from concurrent goroutines with the same txnID.
	if info.binding {
		m.mu.Unlock()
		return nil, fmt.Errorf("transaction %d bind in progress", txnID)
	}
	info.binding = true
	m.mu.Unlock()

	// Bind: acquire pinned connection and BEGIN IMMEDIATE.
	ctx := context.Background()
	conn, err := hive.WriteDB().Conn(ctx)
	if err != nil {
		m.mu.Lock()
		info.binding = false
		m.mu.Unlock()
		return nil, fmt.Errorf("acquire write connection: %w", err)
	}

	_, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE")
	if err != nil {
		conn.Close()
		m.mu.Lock()
		info.binding = false
		m.mu.Unlock()
		return nil, fmt.Errorf("BEGIN IMMEDIATE: %w", err)
	}

	q := &connQuerier{conn: conn}

	m.mu.Lock()
	info.hive = hive
	info.conn = conn
	info.querier = q
	info.binding = false
	m.mu.Unlock()

	return q, nil
}

// addCommitHook registers a function to run after successful commit.
func (m *txnManager) addCommitHook(txnID uint64, fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if info := m.txns[txnID]; info != nil {
		info.onCommit = append(info.onCommit, fn)
	}
}

// addAbortHook registers a function to run after abort/rollback.
func (m *txnManager) addAbortHook(txnID uint64, fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if info := m.txns[txnID]; info != nil {
		info.onAbort = append(info.onAbort, fn)
	}
}

// hiveBusy returns true if any transaction is bound to the given hive.
func (m *txnManager) hiveBusy(hive *hivedb.HiveDB) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, info := range m.txns {
		if info.hive == hive && info.conn != nil {
			return true
		}
	}
	return false
}

// getReadQuerier returns the pinned querier a read should use for a
// transaction. The bool reports whether the caller should use the returned
// querier (true) or fall back to the normal read pool (false).
//
//   - Read-write, bound: the pinned write connection (read-your-own-writes).
//   - Read-write, unbound: pool (no uncommitted writes to see).
//   - Read-only: a pinned snapshot connection, bound lazily on first read
//     (BEGIN DEFERRED on a dedicated connection; WAL fixes the snapshot at
//     that first read). Subsequent reads reuse it for a stable point-in-time
//     view (PSD-005 §7.2).
//   - Unknown txnID: pool.
//
// On SQLITE_BUSY during read-only bind, the error is detectable via isBusy().
func (m *txnManager) getReadQuerier(txnID uint64, hive *hivedb.HiveDB) (Querier, bool, error) {
	m.mu.Lock()
	info := m.txns[txnID]
	if info == nil {
		m.mu.Unlock()
		return nil, false, nil
	}

	// Already bound (read-write write conn, or read-only snapshot conn).
	if info.conn != nil {
		if info.hive != hive {
			m.mu.Unlock()
			return nil, false, fmt.Errorf("transaction %d bound to different hive", txnID)
		}
		q := info.querier
		m.mu.Unlock()
		return q, true, nil
	}

	// Unbound read-write: reads see the committed state via the pool.
	if !info.readOnly {
		m.mu.Unlock()
		return nil, false, nil
	}

	// Unbound read-only: bind a snapshot on this first read.
	if info.binding {
		m.mu.Unlock()
		return nil, false, fmt.Errorf("transaction %d bind in progress", txnID)
	}
	info.binding = true
	m.mu.Unlock()

	snapDB, err := hive.OpenSnapshotConn()
	if err != nil {
		m.clearBinding(info)
		return nil, false, fmt.Errorf("open snapshot connection: %w", err)
	}
	ctx := context.Background()
	conn, err := snapDB.Conn(ctx)
	if err != nil {
		snapDB.Close()
		m.clearBinding(info)
		return nil, false, fmt.Errorf("acquire snapshot connection: %w", err)
	}
	// BEGIN DEFERRED holds no lock yet; the snapshot is fixed at the first
	// read executed on this connection, which the caller runs next.
	if _, err := conn.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		conn.Close()
		snapDB.Close()
		m.clearBinding(info)
		return nil, false, fmt.Errorf("begin snapshot: %w", err)
	}

	q := &connQuerier{conn: conn}
	m.mu.Lock()
	info.hive = hive
	info.snapshotDB = snapDB
	info.conn = conn
	info.querier = q
	info.binding = false
	m.mu.Unlock()
	return q, true, nil
}

// clearBinding resets the in-progress binding flag after a failed bind.
func (m *txnManager) clearBinding(info *txnInfo) {
	m.mu.Lock()
	info.binding = false
	m.mu.Unlock()
}

// commit commits the transaction. On success, releases the connection
// and removes state. On failure, the transaction remains open for
// retry or abort (per spec: "The transaction remains open for retry
// or abort").
//
// The lock is held across the COMMIT to prevent a concurrent abort()
// from destroying the transaction mid-commit.
func (m *txnManager) commit(txnID uint64) error {
	m.mu.Lock()
	info := m.txns[txnID]
	if info == nil {
		m.mu.Unlock()
		return fmt.Errorf("unknown transaction %d", txnID)
	}
	if info.readOnly {
		// LCS MUST NOT commit a read-only transaction (PSD-005 §7.2).
		// Release the snapshot defensively and report success.
		delete(m.txns, txnID)
		m.mu.Unlock()
		releaseSnapshot(info)
		return nil
	}
	if info.conn == nil {
		// Never bound — nothing to commit. Clean up.
		delete(m.txns, txnID)
		m.mu.Unlock()
		return nil
	}

	_, err := info.conn.ExecContext(context.Background(), "COMMIT")
	if err != nil {
		// Transaction remains open for retry or abort.
		m.mu.Unlock()
		return err
	}

	// Success — remove from map while still holding the lock so
	// a concurrent abort() sees the txnID as already gone.
	delete(m.txns, txnID)
	m.mu.Unlock()

	// Run hooks and close connection outside the lock.
	for _, fn := range info.onCommit {
		fn()
	}
	info.conn.Close()
	return nil
}

// abort rolls back and cleans up the transaction.
// Always succeeds per spec: "Disassociate the txn_id from the write
// connection. Always succeeds."
func (m *txnManager) abort(txnID uint64) {
	m.mu.Lock()
	info := m.txns[txnID]
	delete(m.txns, txnID)
	m.mu.Unlock()

	if info == nil {
		return
	}

	if info.conn != nil {
		_, err := info.conn.ExecContext(context.Background(), "ROLLBACK")
		if err != nil {
			log.Printf("rollback transaction %d: %v (connection will be closed)", txnID, err)
		}
		info.conn.Close()
	}
	// Read-only snapshots run on a dedicated DB; close it too.
	if info.snapshotDB != nil {
		info.snapshotDB.Close()
	}

	// Run abort hooks after ROLLBACK so DB state is consistent
	// when cache cleanup runs.
	for _, fn := range info.onAbort {
		fn()
	}
}

// releaseSnapshot rolls back and closes a read-only snapshot's pinned
// connection and dedicated DB. Safe to call with a never-bound snapshot.
func releaseSnapshot(info *txnInfo) {
	if info.conn != nil {
		info.conn.ExecContext(context.Background(), "ROLLBACK")
		info.conn.Close()
	}
	if info.snapshotDB != nil {
		info.snapshotDB.Close()
	}
}

// isBusy checks if an error indicates SQLite BUSY or LOCKED.
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "database is locked") ||
		strings.Contains(s, "database table is locked") ||
		strings.Contains(s, "SQLITE_BUSY") ||
		strings.Contains(s, "SQLITE_LOCKED")
}
