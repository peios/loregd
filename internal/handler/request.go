package handler

import (
	"context"
	"database/sql"
	"time"

	"github.com/peios/loregd/internal/hivedb"
	"github.com/peios/loregd/internal/rsi"
)

// Local fallback budget for legacy test clients. Negotiated requests carry
// their kernel deadline, including admission and queue time.
const requestTimeout = 25 * time.Second

type request struct {
	rsi.RequestHeader
	ctx     context.Context
	cancel  context.CancelFunc
	release []func()
}

func (h *Handler) startRequest(header rsi.RequestHeader) (*request, error) {
	budget := h.requestBudget
	if header.Session != 0 {
		now := rsi.MonotonicNS()
		budget = 0
		if header.DeadlineNS > now {
			budget = time.Duration(header.DeadlineNS - now)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	r := &request{RequestHeader: header, ctx: ctx, cancel: cancel}
	if header.TxnID != 0 {
		if header.Session != 0 && header.LeaseNS == 0 {
			cancel()
			return nil, errExpiredTxn
		}
		unlock, err := h.txns.lockOperation(ctx, header.TxnID)
		if err != nil {
			cancel()
			return nil, err
		}
		r.release = append(r.release, unlock)
	}
	return r, nil
}

func (r *request) lockHive(h *hivedb.HiveDB, write bool) error {
	unlock, err := h.Gate.Acquire(r.ctx, write)
	if err == nil {
		r.release = append(r.release, unlock)
	}
	return err
}

func (r *request) close() {
	for i := len(r.release) - 1; i >= 0; i-- {
		r.release[i]()
	}
	r.cancel()
}

// Cleanup must not reuse an expired request context. A failed rollback must
// discard the physical connection, never return a live transaction to the pool.
func rollbackConn(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		// ROLLBACK also fails when COMMIT already ended the transaction. A
		// conservative discard is safe in both cases; connection setup runs
		// again when the pool reconnects.
		discardConn(conn)
	}
}

func (h *Handler) finishRequest(r *request, status *uint32) {
	if err := r.ctx.Err(); err != nil && *status != rsi.StatusOK {
		if r.TxnID != 0 && h.txns.failInterrupted(r.TxnID) {
			// SQLite can automatically roll back an interrupted write. Keep
			// the ID poisoned until LCS aborts; never silently rebind it and
			// turn subsequent transaction operations into autocommit writes.
			*status = rsi.StatusStorageError
		} else if *status == rsi.StatusStorageError {
			*status = rsi.StatusTxnBusy
		}
	}
	r.close()
}

// commitConn checks admission once, then waits for a definitive storage result.
// It is shared by RSI transactions and the handler's small local transactions.
// The SQLite busy handler is disabled, so lock contention does not wait here.
func commitConn(ctx context.Context, conn *sql.Conn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := conn.ExecContext(context.Background(), "COMMIT")
	return err
}
