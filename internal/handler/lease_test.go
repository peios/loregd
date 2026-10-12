package handler

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/peios/loregd/internal/rsi"
)

func leasedHeader() rsi.RequestHeader {
	now := rsi.MonotonicNS()
	return rsi.RequestHeader{Session: 7, DeadlineNS: now + uint64(10*time.Second), LeaseNS: now + uint64(time.Second)}
}

func TestLeaseExpiryReclaimsWriterAndSnapshot(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "writer", true: "snapshot"}[readOnly], func(t *testing.T) {
			h, hive := testHandler(t)
			if err := h.txns.beginLeased(991, readOnly, leasedHeader()); err != nil {
				t.Fatal(err)
			}
			if readOnly {
				if _, _, err := h.txns.getReadQuerier(context.Background(), 991, hive); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := h.txns.getOrBindWrite(context.Background(), 991, hive); err != nil {
					t.Fatal(err)
				}
			}
			h.txns.mu.Lock()
			info := h.txns.txns[991]
			info.lease = rsi.MonotonicNS() - 1
			h.txns.mu.Unlock()
			if unlock, err := h.txns.lockOperation(context.Background(), 991); err == nil {
				unlock()
				t.Fatal("expired operation admitted")
			}
			var cleanup sync.WaitGroup
			h.txns.maintainLeases(func(uint64, uint64) (uint64, error) { t.Error("expired lease renewed"); return 0, nil }, &cleanup)
			cleanup.Wait()
			h.txns.mu.Lock()
			exists := h.txns.txns[991] != nil
			h.txns.mu.Unlock()
			if exists {
				t.Fatal("expired transaction retained")
			}
			conn, err := hive.WriteDB().Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
			if st, _ := h.handleCreateKey(rsi.RequestHeader{}, encodeCreateKey(rsi.GUID{99}, "AfterLease", rsi.GUID(hive.RootGUID), []byte{1}, false, false)); st != rsi.StatusOK {
				t.Fatalf("writer remained busy: %d", st)
			}
		})
	}
}

func TestLeaseRenewalCannotExtendLifetimeOrResurrect(t *testing.T) {
	m := newTxnManager()
	defer m.close()
	header := leasedHeader()
	header.LifetimeNS = header.LeaseNS + uint64(time.Second)
	if err := m.beginLeased(1, false, header); err != nil {
		t.Fatal(err)
	}
	var cleanup sync.WaitGroup
	m.maintainLeases(func(session, id uint64) (uint64, error) {
		if session != 7 || id != 1 {
			t.Error("identity lost")
		}
		return header.LeaseNS + uint64(5*time.Second), nil
	}, &cleanup)
	m.mu.Lock()
	info := m.txns[1]
	lifetime := info.lifetime
	renewed := info.lease
	info.lifetime = rsi.MonotonicNS() - 1
	m.mu.Unlock()
	if lifetime != header.LifetimeNS || renewed <= header.LeaseNS {
		t.Fatal("renewal changed lifetime or failed")
	}
	m.maintainLeases(func(uint64, uint64) (uint64, error) { t.Error("renewed past lifetime"); return 0, nil }, &cleanup)
	cleanup.Wait()
	// A response that arrives after local expiry must be discarded, even if
	// it was granted by the kernel just before its own deadline.
	if err := m.beginLeased(2, false, leasedHeader()); err != nil {
		t.Fatal(err)
	}
	m.maintainLeases(func(uint64, uint64) (uint64, error) {
		m.mu.Lock()
		m.txns[2].lease = rsi.MonotonicNS() - 1
		m.mu.Unlock()
		return rsi.MonotonicNS() + uint64(5*time.Second), nil
	}, &cleanup)
	m.mu.Lock()
	expired := m.txns[2].expired
	m.mu.Unlock()
	if !expired {
		t.Fatal("late renewal resurrected transaction")
	}
}

func TestOwnershipLossReclaimsWithoutAbortMessage(t *testing.T) {
	m := newTxnManager()
	defer m.close()
	if err := m.beginLeased(1, false, leasedHeader()); err != nil {
		t.Fatal(err)
	}
	var cleanup sync.WaitGroup
	denied := func(uint64, uint64) (uint64, error) { return 0, errors.New("owner released") }
	m.maintainLeases(denied, &cleanup)
	if unlock, err := m.lockOperation(context.Background(), 1); err == nil {
		unlock()
		t.Fatal("ownerless operation admitted")
	}
	m.maintainLeases(denied, &cleanup)
	cleanup.Wait()
	m.mu.Lock()
	n := len(m.txns)
	m.mu.Unlock()
	if n != 0 {
		t.Fatal("ownerless transaction retained")
	}
}

func TestExpiryWaitsForAdmittedCommitOutcome(t *testing.T) {
	h, hive := testHandler(t)
	if err := h.txns.beginLeased(1, false, leasedHeader()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.txns.getOrBindWrite(context.Background(), 1, hive); err != nil {
		t.Fatal(err)
	}
	committed := make(chan struct{})
	release := make(chan struct{})
	aborted := make(chan struct{}, 1)
	h.txns.addCommitHook(1, func() { close(committed); <-release })
	h.txns.addAbortHook(1, func() { aborted <- struct{}{} })
	done := make(chan error, 1)
	go func() { done <- h.txns.commit(context.Background(), 1) }()
	<-committed
	h.txns.mu.Lock()
	h.txns.txns[1].lease = rsi.MonotonicNS() - 1
	h.txns.mu.Unlock()
	var cleanup sync.WaitGroup
	h.txns.maintainLeases(func(uint64, uint64) (uint64, error) { t.Error("renewed terminal commit"); return 0, nil }, &cleanup)
	select {
	case <-aborted:
		t.Fatal("expiry rolled back an unresolved commit")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cleanup.Wait()
	select {
	case <-aborted:
		t.Fatal("successful commit treated as abort")
	default:
	}
}

func TestNegotiatedRequestUsesRemainingKernelDeadline(t *testing.T) {
	h, _ := testHandler(t)
	header := leasedHeader()
	header.DeadlineNS = rsi.MonotonicNS() - 1
	req, err := h.startRequest(header)
	if err != nil {
		t.Fatal(err)
	}
	defer req.close()
	if !errors.Is(req.ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("expired kernel deadline reset to local timeout")
	}
	if err := h.txns.beginLeased(1, false, header); err == nil {
		t.Fatal("expired BEGIN admitted")
	}
	header = leasedHeader()
	header.LeaseNS = 0
	if err := h.txns.beginLeased(2, false, header); err == nil {
		t.Fatal("BEGIN without live owner admitted")
	}
}

// A controlled driver models COMMIT blocked in durability I/O. Cancellation
// after admission must not become a false failure or race an expiry rollback.
type commitConnector struct {
	entered chan context.Context
	release chan struct{}
}

func (c *commitConnector) Connect(context.Context) (driver.Conn, error) {
	return &commitConnection{c}, nil
}
func (c *commitConnector) Driver() driver.Driver { return commitDriver{} }

type commitDriver struct{}

func (commitDriver) Open(string) (driver.Conn, error) { return nil, errors.New("unused") }

type commitConnection struct{ c *commitConnector }

func (*commitConnection) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (*commitConnection) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (*commitConnection) Close() error                        { return nil }
func (c *commitConnection) ExecContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if query != "COMMIT" {
		return nil, fmt.Errorf("unexpected SQL %s", query)
	}
	c.c.entered <- ctx
	<-c.c.release
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return driver.RowsAffected(0), nil
}

func TestAdmittedCommitOutlivesRequestCancellationAndLeaseExpiry(t *testing.T) {
	connector := &commitConnector{entered: make(chan context.Context, 1), release: make(chan struct{})}
	db := sql.OpenDB(connector)
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := newTxnManager()
	defer m.close()
	if err := m.beginLeased(1, false, leasedHeader()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.txns[1].conn = conn
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- m.commit(ctx, 1) }()
	sqlContext := <-connector.entered
	cancel()
	m.mu.Lock()
	m.txns[1].lease = rsi.MonotonicNS() - 1
	m.mu.Unlock()
	var cleanup sync.WaitGroup
	m.maintainLeases(func(uint64, uint64) (uint64, error) { t.Error("expired commit renewed"); return 0, nil }, &cleanup)
	if sqlContext.Err() != nil {
		t.Error("caller cancellation interrupted admitted SQL COMMIT")
	}
	if _, limited := sqlContext.Deadline(); limited {
		t.Error("admitted COMMIT inherited caller deadline")
	}
	close(connector.release)
	if err := <-result; err != nil {
		t.Fatalf("lost definitive commit outcome: %v", err)
	}
	cleanup.Wait()
}
