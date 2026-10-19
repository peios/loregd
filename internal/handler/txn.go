package handler

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/peios/loregd/internal/hivedb"
	"github.com/peios/loregd/internal/rsi"
)

// errReadOnlyTxn is returned when a mutating operation is tagged with a
// read-only transaction ID. Per PSD-005 §7.2, loregd MUST reject it with
// RSI_INVALID and MUST NOT mutate state.
var errReadOnlyTxn = errors.New("mutating operation on read-only transaction")

// txnInfo tracks state for one active RSI transaction.
type txnInfo struct {
	session, leaseNS, lifetimeNS uint64
	expired                      bool
	cleanupStarted               bool
	hive                         *hivedb.HiveDB // nil = unbound (no operation has bound a connection yet)
	conn                         *sql.Conn      // pinned connection (write conn for RW, snapshot conn for RO)
	operation                    hivedb.Gate
	binding                      bool // true while bind is in progress (prevents double-bind)

	failed     bool    // interrupted operation; only abort may release this ID
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
	stop chan struct{}
	done chan struct{}
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
func (m *txnManager) getOrBindWrite(ctx context.Context, txnID uint64, hive *hivedb.HiveDB) (Querier, error) {
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

	if info.failed {
		m.mu.Unlock()
		return nil, errInterruptedTxn
	}

	// Already bound — verify same hive and return existing querier.
	if info.conn != nil {
		if info.hive != hive {
			m.mu.Unlock()
			return nil, fmt.Errorf("transaction %d bound to different hive", txnID)
		}
		q := &connQuerier{conn: info.conn, ctx: ctx}
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
	conn, err := hive.WriteDB().Conn(ctx)
	if err != nil {
		m.mu.Lock()
		info.binding = false
		m.mu.Unlock()
		return nil, fmt.Errorf("acquire write connection: %w", err)
	}

	_, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE")
	if err != nil {
		rollbackConn(conn)
		releaseConnection(conn)
		m.mu.Lock()
		info.binding = false
		m.mu.Unlock()
		return nil, fmt.Errorf("BEGIN IMMEDIATE: %w", err)
	}

	q := &connQuerier{conn: conn, ctx: ctx}

	m.mu.Lock()
	info.hive = hive
	info.conn = conn
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

// mutationAllowed reports whether a mutating operation tagged with txnID may
// proceed, giving the same answers getOrBindWrite gives every handler that
// routes through writeQ. A zero txnID is non-transactional and always allowed;
// an unknown one is an error; a read-only one is errReadOnlyTxn.
//
// It exists for the operations that cannot take a transaction's pinned
// connection — RSI_DELETE_LAYER spans every hive, and RSI_FLUSH checkpoints one
// — which previously did not consult hdr.TxnID at all and so mutated happily
// under a read-only transaction (PEI-231).
func (m *txnManager) mutationAllowed(txnID uint64) error {
	if txnID == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	info := m.txns[txnID]
	if info == nil {
		return fmt.Errorf("unknown transaction %d", txnID)
	}
	if info.readOnly {
		return errReadOnlyTxn
	}
	return nil
}

// anyWriteBound reports whether any transaction currently holds a pinned write
// connection, on any hive. hiveBusy answers this for one hive; RSI_DELETE_LAYER
// touches all of them, and MaxOpenConns is 1 per hive, so it must ask about all
// of them or block forever on the one that is taken (PEI-230).
func (m *txnManager) anyWriteBound() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, info := range m.txns {
		if !info.readOnly && info.conn != nil {
			return true
		}
	}
	return false
}

// hiveBusy reports whether a transaction holds this hive's write connection.
func (m *txnManager) hiveBusy(hive *hivedb.HiveDB) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, info := range m.txns {
		if !info.readOnly && info.hive == hive && info.conn != nil {
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
func (m *txnManager) getReadQuerier(ctx context.Context, txnID uint64, hive *hivedb.HiveDB) (Querier, bool, error) {
	m.mu.Lock()
	info := m.txns[txnID]
	if info == nil {
		m.mu.Unlock()
		return nil, false, nil
	}

	if info.failed {
		m.mu.Unlock()
		return nil, false, errInterruptedTxn
	}

	// Already bound (read-write write conn, or read-only snapshot conn).
	if info.conn != nil {
		if info.hive != hive {
			m.mu.Unlock()
			return nil, false, fmt.Errorf("transaction %d bound to different hive", txnID)
		}
		q := &connQuerier{conn: info.conn, ctx: ctx}
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

	conn, snapDB, err := openPinnedSnapshot(ctx, hive)
	if err != nil {
		m.clearBinding(info)
		return nil, false, err
	}

	q := &connQuerier{conn: conn, ctx: ctx}
	m.mu.Lock()
	info.hive = hive
	info.snapshotDB = snapDB
	info.conn = conn
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
// The transaction operation gate excludes concurrent operations/abort;
// the hive publication gate excludes readers only during COMMIT.
func (m *txnManager) commit(ctx context.Context, txnID uint64) error {
	unlock, err := m.lockOperation(ctx, txnID)
	if err != nil {
		return err
	}
	defer unlock()
	m.mu.Lock()
	info := m.txns[txnID]
	m.mu.Unlock()
	if info.failed {
		return errInterruptedTxn
	}
	if info.hive != nil {
		release, err := info.hive.Gate.Acquire(ctx, true)
		if err != nil {
			return err
		}
		defer release()
	}
	m.mu.Lock()
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return err
	}
	if info.leaseExpired(rsi.MonotonicNS()) {
		m.mu.Unlock()
		return errExpiredTxn
	}
	m.mu.Unlock()
	if info.readOnly {
		releaseSnapshot(info)
	} else if info.conn != nil {
		// Admission obeys the request deadline; an admitted COMMIT must
		// resolve its storage outcome even if the caller or lease expires.
		// Canceling here could report interruption after durable publication.
		// SQLite's busy handler is disabled, so contention still returns busy.
		if err := commitConn(ctx, info.conn); err != nil {
			return err
		}
		for _, fn := range info.onCommit {
			fn()
		}
		releaseConnection(info.conn)
	}
	m.mu.Lock()
	delete(m.txns, txnID)
	m.mu.Unlock()
	return nil
}

// abort rolls back and cleans up the transaction.
// Always succeeds per spec: "Disassociate the txn_id from the write
// connection. Always succeeds."
func (m *txnManager) abort(txnID uint64) {
	// Abort is cleanup owed to LCS even if the original request expired.
	unlock, err := m.lockOperationForCleanup(context.Background(), txnID)
	if err != nil {
		return
	}
	defer unlock()
	m.mu.Lock()
	info := m.txns[txnID]
	m.mu.Unlock()
	if info.hive != nil {
		release, _ := info.hive.Gate.Acquire(context.Background(), true)
		defer release()
	}
	m.mu.Lock()
	delete(m.txns, txnID)
	m.mu.Unlock()

	if info == nil {
		return
	}

	if info.conn != nil {
		rollbackConn(info.conn)
		releaseConnection(info.conn)
	}
	// Read-only snapshots run on a dedicated DB; close it too.
	if info.snapshotDB != nil {
		closeSnapshotDatabase(info.snapshotDB)
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
		rollbackConn(info.conn)
		releaseConnection(info.conn)
	}
	if info.snapshotDB != nil {
		closeSnapshotDatabase(info.snapshotDB)
	}
}

// isBusy includes Go-side admission/deadline failures and SQLite primary codes.
var errWriterBusy = errors.New("hive write connection is busy")

func isBusy(err error) bool {
	if errors.Is(err, errWriterBusy) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code() & 0xff
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}

func discardConn(conn *sql.Conn) {
	// ErrBadConn tells database/sql to discard the physical connection. Raw
	// returns that same sentinel; there is no additional recovery to perform.
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}

type operationMode uint8

const (
	normalOperation operationMode = iota
	cleanupOperation
)

func (m *txnManager) lockOperation(ctx context.Context, id uint64) (func(), error) {
	return m.lockOperationMode(ctx, id, normalOperation)
}
func (m *txnManager) lockOperationForCleanup(ctx context.Context, id uint64) (func(), error) {
	return m.lockOperationMode(ctx, id, cleanupOperation)
}
func (m *txnManager) lockOperationMode(ctx context.Context, id uint64, mode operationMode) (func(), error) {
	m.mu.Lock()
	info := m.txns[id]
	m.mu.Unlock()
	if info == nil {
		return nil, fmt.Errorf("unknown transaction %d", id)
	}
	unlock, err := info.operation.Acquire(ctx, true)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	current := m.txns[id]
	expired := mode != cleanupOperation && info.leaseExpired(rsi.MonotonicNS())
	m.mu.Unlock()
	if current != info || expired {
		unlock()
		return nil, fmt.Errorf("transaction %d ended", id)
	}
	return unlock, nil
}

// Caller holds the hive publication gate, so admission cannot race another bind.
func (m *txnManager) otherWriter(hive *hivedb.HiveDB, id uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for txnID, info := range m.txns {
		if txnID != id && !info.readOnly && info.hive == hive && info.conn != nil {
			return true
		}
	}
	return false
}

// Close runs only after dispatch drains, releasing every pinned connection.
func (m *txnManager) close() {
	if m.stop != nil {
		close(m.stop)
		<-m.done
	}
	m.mu.Lock()
	ids := make([]uint64, 0, len(m.txns))
	for id := range m.txns {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.abort(id)
	}
}

var errInterruptedTxn = errors.New("transaction interrupted; abort required")

// Caller holds the transaction's operation gate. Do not expire a healthy
// transaction using an invented daemon TTL: LCS owns its lifetime.
func (m *txnManager) failInterrupted(id uint64) bool {
	m.mu.Lock()
	info := m.txns[id]
	m.mu.Unlock()
	if info == nil || info.conn == nil {
		return false
	}
	rollbackConn(info.conn)
	releaseConnection(info.conn)
	if info.snapshotDB != nil {
		closeSnapshotDatabase(info.snapshotDB)
	}
	m.mu.Lock()
	info.conn = nil
	info.snapshotDB = nil
	info.failed = true
	m.mu.Unlock()
	return true
}

func storageStatus(err error) uint32 {
	if isBusy(err) {
		return rsi.StatusTxnBusy
	}
	return rsi.StatusStorageError
}

// GUID cache misses must consult a bound transaction's own view. In particular,
// deleting a key outside a backup evicts its cache entry, but cannot make that
// key disappear from the backup's already-pinned snapshot.
func (m *txnManager) boundQuerier(id uint64, hive *hivedb.HiveDB, ctx context.Context) Querier {
	m.mu.Lock()
	defer m.mu.Unlock()
	if info := m.txns[id]; info != nil && info.hive == hive && info.conn != nil && !info.failed {
		return &connQuerier{conn: info.conn, ctx: ctx}
	}
	return nil
}

// openPinnedSnapshot opens and reads both attached stores while the caller holds
// the hive publication gate, fixing one coherent read-only view.
func openPinnedSnapshot(ctx context.Context, hive *hivedb.HiveDB) (*sql.Conn, *sql.DB, error) {
	snapDB, err := hive.OpenSnapshotConn(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("open snapshot connection: %w", err)
	}
	conn, err := snapDB.Conn(ctx)
	if err != nil {
		closeSnapshotDatabase(snapDB)
		return nil, nil, fmt.Errorf("acquire snapshot connection: %w", err)
	}
	// BEGIN DEFERRED alone fixes no snapshot. Read both schemas under the
	// hive gate before releasing this request to a concurrent publisher.
	if _, err := conn.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		rollbackConn(conn)
		releaseConnection(conn)
		closeSnapshotDatabase(snapDB)
		return nil, nil, fmt.Errorf("begin snapshot: %w", err)
	}

	var mainCount, volatileCount int
	err = conn.QueryRowContext(ctx, `
        SELECT EXISTS(SELECT 1 FROM main.keys), EXISTS(SELECT 1 FROM volatile.keys)
    `).Scan(&mainCount, &volatileCount)
	if err != nil {
		rollbackConn(conn)
		releaseConnection(conn)
		closeSnapshotDatabase(snapDB)
		return nil, nil, fmt.Errorf("pin both snapshot stores: %w", err)
	}

	return conn, snapDB, nil
}
