package handler

import (
	"context"
	"testing"
	"time"

	"github.com/peios/loregd/internal/rsi"
)

func promptly(t *testing.T, f func() uint32) uint32 {
	t.Helper()
	done := make(chan uint32, 1)
	go func() { done <- f() }()
	select {
	case st := <-done:
		return st
	case <-time.After(2 * time.Second):
		t.Fatal("operation waited for an open transaction")
		return 0
	}
}

// PEI-1241: even an empty DELETE of volatile.path_entries used to acquire a
// shared-cache table lock and stall a sibling lookup until the writer ended.
func TestLookupWhileDeleteTransactionOpen(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	one, two := rsi.GUID{0x91}, rsi.GUID{0x92}
	for _, item := range []struct {
		name string
		id   rsi.GUID
	}{{"One", one}, {"Two", two}} {
		insertKey(t, hive, item.id, item.name, root, false)
		insertPathEntry(t, hive, root, item.name, "base", item.id, 1)
	}
	if status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxn(301)); status != rsi.StatusOK {
		t.Fatalf("fixture operation status = %d, want OK", status)
	}
	defer h.txns.abort(301)
	st, _ := h.handleDeleteEntry(rsi.RequestHeader{TxnID: 301}, encodeDeleteEntry(root, "One", "base"))
	if st != rsi.StatusOK {
		t.Fatalf("delete: %d", st)
	}
	for _, name := range []string{"One", "Two"} {
		st := promptly(t, func() uint32 { st, _ := h.handleLookup(rsi.RequestHeader{}, encodeLookup(root, name)); return st })
		if st != rsi.StatusOK {
			t.Fatalf("lookup %s while writer open: %d", name, st)
		}
	}
	// Competing writes, including paths that acquire their own connection,
	// decline promptly instead of sitting in database/sql's single-slot pool.
	for _, f := range []func() uint32{
		func() uint32 {
			st, _ := h.handleCreateKey(rsi.RequestHeader{}, encodeCreateKey(rsi.GUID{0x93}, "Busy", root, []byte{1}, false, false))
			return st
		},
		func() uint32 { st, _ := h.handleDropKey(rsi.RequestHeader{}, encodeDropKey(two)); return st },
		func() uint32 { st, _ := h.handleFlush(rsi.RequestHeader{}, encodeFlush(hive.Name)); return st },
		func() uint32 { st, _ := h.handleDeleteLayer(rsi.RequestHeader{}, encodeDeleteLayer("base")); return st },
	} {
		if st := promptly(t, f); st != rsi.StatusTxnBusy {
			t.Fatalf("contending write: %d", st)
		}
	}
	h.txns.abort(301)
	st, _ = h.handleDeleteEntry(rsi.RequestHeader{}, encodeDeleteEntry(root, "One", "base"))
	if st != rsi.StatusOK {
		t.Fatalf("write after abort: %d", st)
	}
}

func TestSnapshotPinsBothStoresAndDoesNotBlockVolatileWriter(t *testing.T) {
	h, hive, persistent := setupKeyForValues(t)
	volatile := rsi.GUID{0x94}
	insertKey(t, hive, volatile, "Volatile", rsi.GUID(hive.RootGUID), true)
	insertValue(t, hive, persistent, "Value", "base", 1, []byte("old"), 1)
	if _, err := hive.WriteDB().Exec(`INSERT INTO volatile.[values] (key_guid,name,name_folded,layer,type,data,sequence) VALUES (?,'Value','value','base',1,?,1)`, volatile[:], []byte("old")); err != nil {
		t.Fatal(err)
	}
	if status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxnMode(302, rsi.TxnReadOnly)); status != rsi.StatusOK {
		t.Fatalf("fixture operation status = %d, want OK", status)
	}
	defer h.txns.abort(302)
	hdr := rsi.RequestHeader{TxnID: 302}
	if val, _ := queryFirstValueData(t, h, hdr, persistent, "Value"); val != "old" {
		t.Fatal(val)
	}
	if st := promptly(t, func() uint32 {
		st, _ := h.handleSetValue(rsi.RequestHeader{}, encodeSetValue(volatile, "Value", "base", 1, []byte("new"), 2, 0))
		return st
	}); st != rsi.StatusOK {
		t.Fatalf("volatile write during backup: %d", st)
	}
	// The first read visited only main, but volatile must share that instant.
	if val, _ := queryFirstValueData(t, h, hdr, volatile, "Value"); val != "old" {
		t.Fatalf("split snapshot: %q", val)
	}
	if val, _ := queryFirstValueData(t, h, rsi.RequestHeader{}, volatile, "Value"); val != "new" {
		t.Fatalf("committed value: %q", val)
	}
	if st := promptly(t, func() uint32 {
		st, _ := h.handleDeleteLayer(rsi.RequestHeader{}, encodeDeleteLayer("absent"))
		return st
	}); st != rsi.StatusOK {
		t.Fatalf("snapshot mistaken for writer: %d", st)
	}
}

func TestPoolWaitHasRequestDeadline(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	h.requestBudget = 30 * time.Millisecond
	conn, err := hive.WriteDB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st := promptly(t, func() uint32 {
		st, _ := h.handleSetValue(rsi.RequestHeader{}, encodeSetValue(guid, "X", "base", 1, []byte("x"), 1, 0))
		return st
	})
	if st != rsi.StatusTxnBusy {
		t.Fatalf("pool timeout status = %d", st)
	}
	conn.Close()
	st, _ = h.handleSetValue(rsi.RequestHeader{}, encodeSetValue(guid, "X", "base", 1, []byte("x"), 1, 0))
	if st != rsi.StatusOK {
		t.Fatalf("pool recovery = %d", st)
	}
}

func TestRequestWaitsForWholePublication(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	insertValue(t, hive, guid, "Value", "base", 1, []byte("before"), 1)
	unlock, err := hive.Gate.Acquire(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan uint32, 1)
	go func() {
		st, _ := h.handleQueryValues(rsi.RequestHeader{}, encodeQueryValues(guid, "Value", false))
		done <- st
	}()
	select {
	case <-done:
		unlock()
		t.Fatal("reader entered during publication")
	case <-time.After(30 * time.Millisecond):
	}
	unlock()
	select {
	case st := <-done:
		if st != rsi.StatusOK {
			t.Fatal(st)
		}
	case <-time.After(time.Second):
		t.Fatal("reader not released")
	}
}

// A canceled SQLite write may have rolled back the entire transaction. Its ID
// must never be reused as an unbound transaction or an autocommit connection.
func TestInterruptedTransactionCannotResumeOrCommit(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	if status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxn(303)); status != rsi.StatusOK {
		t.Fatalf("fixture operation status = %d, want OK", status)
	}
	st, _ := h.handleCreateKey(rsi.RequestHeader{TxnID: 303}, encodeCreateKey(rsi.GUID{0x96}, "Interrupted", root, []byte{1}, false, false))
	if st != rsi.StatusOK {
		t.Fatal(st)
	}
	r, err := h.startRequest(rsi.RequestHeader{TxnID: 303})
	if err != nil {
		t.Fatal(err)
	}
	r.cancel()
	st = rsi.StatusStorageError
	h.finishRequest(r, &st)
	if st != rsi.StatusStorageError {
		t.Fatal(st)
	}
	st, _ = h.handleCreateKey(rsi.RequestHeader{TxnID: 303}, encodeCreateKey(rsi.GUID{0x97}, "MustNotCommit", root, []byte{1}, false, false))
	if st != rsi.StatusStorageError {
		t.Fatalf("resumed interrupted transaction: %d", st)
	}
	if err := h.txns.commit(context.Background(), 303); err == nil {
		t.Fatal("committed interrupted transaction")
	}
	var count int
	if err := hive.ReadDB().QueryRow("SELECT COUNT(*) FROM main.keys WHERE name IN ('Interrupted','MustNotCommit')").Scan(&count); err != nil || count != 0 {
		t.Fatalf("leaked mutation: %d %v", count, err)
	}
	h.txns.abort(303)
	st, _ = h.handleCreateKey(rsi.RequestHeader{}, encodeCreateKey(rsi.GUID{0x98}, "Recovered", root, []byte{1}, false, false))
	if st != rsi.StatusOK {
		t.Fatalf("writer not released: %d", st)
	}
}

func TestSQLiteWriteLockHasRequestDeadline(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	h.requestBudget = 30 * time.Millisecond
	external, err := hive.OpenSnapshotConn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	if _, err := external.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer external.Exec("ROLLBACK")
	st := promptly(t, func() uint32 {
		st, _ := h.handleSetValue(rsi.RequestHeader{}, encodeSetValue(guid, "X", "base", 1, []byte("x"), 1, 0))
		return st
	})
	if st != rsi.StatusTxnBusy {
		t.Fatalf("SQLite write-lock timeout: %d", st)
	}
}

func TestSnapshotStillResolvesDeletedKeyAfterCacheEviction(t *testing.T) {
	h, _, guid := setupKeyForValues(t)
	if status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxnMode(304, rsi.TxnReadOnly)); status != rsi.StatusOK {
		t.Fatalf("fixture operation status = %d, want OK", status)
	}
	defer h.txns.abort(304)
	hdr := rsi.RequestHeader{TxnID: 304}
	st, _ := h.handleReadKey(hdr, encodeReadKey(guid))
	if st != rsi.StatusOK {
		t.Fatal(st)
	}
	st, _ = h.handleDropKey(rsi.RequestHeader{}, encodeDropKey(guid))
	if st != rsi.StatusOK {
		t.Fatal(st)
	}
	st, _ = h.handleReadKey(hdr, encodeReadKey(guid))
	if st != rsi.StatusOK {
		t.Fatalf("snapshot lost a deleted key after GUID cache eviction: %d", st)
	}
	st, _ = h.handleReadKey(rsi.RequestHeader{}, encodeReadKey(guid))
	if st != rsi.StatusNotFound {
		t.Fatalf("current view did not see deletion: %d", st)
	}
}
