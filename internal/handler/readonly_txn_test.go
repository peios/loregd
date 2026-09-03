package handler

import (
	"testing"

	"github.com/peios/loregd/internal/rsi"
)

// encodeBeginTxnMode builds an RSI_BEGIN_TRANSACTION payload with an explicit
// mode field (txn_id + mode), as LCS sends it.
func encodeBeginTxnMode(txnID uint64, mode uint32) []byte {
	e := rsi.NewEncoder(12)
	e.PutUint64(txnID)
	e.PutUint32(mode)
	return e.Bytes()
}

// queryFirstValueData reads the first value entry returned by RSI_QUERY_VALUES
// and returns its data and whether any entry was present.
func queryFirstValueData(t *testing.T, h *Handler, hdr rsi.RequestHeader, guid rsi.GUID, name string) (string, bool) {
	t.Helper()
	status, payload := h.handleQueryValues(hdr, encodeQueryValues(guid, name, false))
	if status != rsi.StatusOK {
		t.Fatalf("query values: status = %d", status)
	}
	d := rsi.NewDecoder(payload)
	count, _ := d.Uint32()
	if count == 0 {
		return "", false
	}
	_, _ = d.String() // value_name
	_, _ = d.String() // layer_name
	_, _ = d.Uint32() // type
	data, _ := d.Blob()
	return string(data), true
}

// TestReadOnlyTxnRejectsMutation: a mutating op tagged with a read-only
// transaction MUST be rejected with RSI_INVALID and MUST NOT mutate state
// (PSD-005 §7.2). Covers both the values.go and handler.go write paths.
func TestReadOnlyTxnRejectsMutation(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)

	status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxnMode(81, rsi.TxnReadOnly))
	if status != rsi.StatusOK {
		t.Fatalf("begin read-only: status = %d", status)
	}
	hdr := rsi.RequestHeader{TxnID: 81}

	// RSI_SET_VALUE (values.go path).
	status, _ = h.handleSetValue(hdr, encodeSetValue(guid, "Nope", "base", 1, []byte("x"), 5, 0))
	if status != rsi.StatusInvalid {
		t.Errorf("set value on read-only txn: status = %d, want StatusInvalid", status)
	}

	// RSI_CREATE_KEY (handler.go path).
	status, _ = h.handleCreateKey(hdr, encodeCreateKey(rsi.GUID{0xB0}, "NoCreate", rsi.GUID(hive.RootGUID), []byte{0x01}, false, false))
	if status != rsi.StatusInvalid {
		t.Errorf("create key on read-only txn: status = %d, want StatusInvalid", status)
	}

	// Nothing should have been written.
	var values, keys int
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM main.[values] WHERE key_guid = ? AND name_folded = ?`, guid[:], "nope").Scan(&values)
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM main.keys WHERE name_folded = ?`, "nocreate").Scan(&keys)
	if values != 0 {
		t.Errorf("value count = %d, want 0 (no mutation)", values)
	}
	if keys != 0 {
		t.Errorf("key count = %d, want 0 (no mutation)", keys)
	}

	h.handleAbortTransaction(rsi.RequestHeader{}, encodeAbortTxn(81))
}

// TestReadOnlyTxnSnapshotIsolation: reads tagged with a read-only transaction
// observe a stable point-in-time snapshot — a value committed externally
// after the snapshot's first read is invisible within the transaction
// (PSD-005 §7.2).
func TestReadOnlyTxnSnapshotIsolation(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	insertValue(t, hive, guid, "Snap", "base", 1, []byte("v1"), 10)

	status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxnMode(80, rsi.TxnReadOnly))
	if status != rsi.StatusOK {
		t.Fatalf("begin read-only: status = %d", status)
	}
	hdr := rsi.RequestHeader{TxnID: 80}

	// First read fixes the snapshot at v1.
	if val, ok := queryFirstValueData(t, h, hdr, guid, "Snap"); !ok || val != "v1" {
		t.Fatalf("snapshot first read = %q (present=%v), want v1", val, ok)
	}

	// Commit a new value OUTSIDE the transaction.
	status, _ = h.handleSetValue(rsi.RequestHeader{}, encodeSetValue(guid, "Snap", "base", 1, []byte("v2"), 20, 0))
	if status != rsi.StatusOK {
		t.Fatalf("external set value: status = %d", status)
	}

	// Re-read within the read-only transaction: still v1 (stable snapshot).
	if val, ok := queryFirstValueData(t, h, hdr, guid, "Snap"); !ok || val != "v1" {
		t.Errorf("snapshot re-read = %q (present=%v), want v1 (stable)", val, ok)
	}

	// Releasing the snapshot via ABORT, a fresh read sees v2.
	h.handleAbortTransaction(rsi.RequestHeader{}, encodeAbortTxn(80))
	if val, ok := queryFirstValueData(t, h, rsi.RequestHeader{}, guid, "Snap"); !ok || val != "v2" {
		t.Errorf("post-abort read = %q (present=%v), want v2", val, ok)
	}
}

// TestReadOnlyTxnReleaseAllowsReuse: after a read-only transaction binds a
// snapshot, both ABORT and the defensive COMMIT-on-read-only path release it,
// so the transaction ID can be reused (the snapshot conn and dedicated DB are
// freed; otherwise the second begin would fail as already-active).
func TestReadOnlyTxnReleaseAllowsReuse(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	insertValue(t, hive, guid, "R", "base", 1, []byte("a"), 1)

	// Cycle 1: begin → read (binds snapshot) → ABORT.
	if status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxnMode(90, rsi.TxnReadOnly)); status != rsi.StatusOK {
		t.Fatalf("begin #1: status = %d", status)
	}
	if _, ok := queryFirstValueData(t, h, rsi.RequestHeader{TxnID: 90}, guid, "R"); !ok {
		t.Fatal("read #1 saw no value")
	}
	if status, _ := h.handleAbortTransaction(rsi.RequestHeader{}, encodeAbortTxn(90)); status != rsi.StatusOK {
		t.Fatalf("abort #1: status = %d", status)
	}

	// Cycle 2: same ID begins cleanly → read → release via COMMIT (LCS
	// shouldn't send this for read-only, but loregd must handle it).
	if status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxnMode(90, rsi.TxnReadOnly)); status != rsi.StatusOK {
		t.Fatalf("begin #2 (reused ID): status = %d", status)
	}
	if _, ok := queryFirstValueData(t, h, rsi.RequestHeader{TxnID: 90}, guid, "R"); !ok {
		t.Fatal("read #2 saw no value")
	}
	if status, _ := h.handleCommitTransaction(rsi.RequestHeader{}, encodeCommitTxn(90)); status != rsi.StatusOK {
		t.Fatalf("commit #2 (read-only release): status = %d", status)
	}

	// Cycle 3: ID is free again.
	if status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxnMode(90, rsi.TxnReadOnly)); status != rsi.StatusOK {
		t.Fatalf("begin #3 (reused ID after commit): status = %d", status)
	}
	h.handleAbortTransaction(rsi.RequestHeader{}, encodeAbortTxn(90))
}

// TestReadOnlyTxnRejectsDeleteLayerAndFlush: RSI_DELETE_LAYER and RSI_FLUSH
// never consulted hdr.TxnID at all, so they executed happily under a read-only
// transaction — DELETE_LAYER actually deleting data (PEI-231). They cannot take
// the transaction's pinned connection (DELETE_LAYER spans every hive, and a
// transaction binds one), so they answer the question directly instead of
// routing through writeQ; the answer must be the same RSI_INVALID.
func TestReadOnlyTxnRejectsDeleteLayerAndFlush(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0xD1}

	insertKey(t, hive, guid, "LayerChild", root, false)
	insertPathEntry(t, hive, root, "LayerChild", "role-x", guid, 1)
	insertPathEntry(t, hive, root, "LayerChild", "base", guid, 0)

	status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxnMode(91, rsi.TxnReadOnly))
	if status != rsi.StatusOK {
		t.Fatalf("begin read-only: status = %d", status)
	}
	hdr := rsi.RequestHeader{TxnID: 91}

	status, _ = h.handleDeleteLayer(hdr, encodeDeleteLayer("role-x"))
	if status != rsi.StatusInvalid {
		t.Errorf("delete layer on read-only txn: status = %d, want StatusInvalid", status)
	}

	status, _ = h.handleFlush(hdr, encodeFlush(hive.Name))
	if status != rsi.StatusInvalid {
		t.Errorf("flush on read-only txn: status = %d, want StatusInvalid", status)
	}

	// The layer must still be there: the status is the symptom, the deletion
	// is the defect, and a check that only asserted the code would pass while
	// the rows were still being removed.
	var entries int
	hive.WriteDB().QueryRow(
		`SELECT COUNT(*) FROM main.path_entries WHERE layer = ?`, "role-x").Scan(&entries)
	if entries != 1 {
		t.Errorf("role-x path entries = %d, want 1 (nothing deleted)", entries)
	}

	h.handleAbortTransaction(rsi.RequestHeader{}, encodeAbortTxn(91))
}

// TestDeleteLayerDeclinesWhileATransactionHoldsAWrite: each hive allows one
// write connection, so RSI_DELETE_LAYER opening its own while a read-write
// transaction holds it blocked indefinitely (PEI-230). It now declines with
// RSI_TXN_BUSY, the same answer RSI_FLUSH already gave.
func TestDeleteLayerDeclinesWhileATransactionHoldsAWrite(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)

	status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxn(92))
	if status != rsi.StatusOK {
		t.Fatalf("begin: status = %d", status)
	}
	hdr := rsi.RequestHeader{TxnID: 92}

	// Bind the write connection with a real mutating op.
	status, _ = h.handleCreateKey(hdr, encodeCreateKey(rsi.GUID{0xD2}, "Bound", root, []byte{0x01}, false, false))
	if status != rsi.StatusOK {
		t.Fatalf("create key in txn: status = %d", status)
	}

	status, _ = h.handleDeleteLayer(rsi.RequestHeader{}, encodeDeleteLayer("role-x"))
	if status != rsi.StatusTxnBusy {
		t.Errorf("delete layer while a write is bound: status = %d, want StatusTxnBusy", status)
	}

	h.handleAbortTransaction(rsi.RequestHeader{}, encodeAbortTxn(92))

	// Once the transaction is gone it proceeds normally.
	status, _ = h.handleDeleteLayer(rsi.RequestHeader{}, encodeDeleteLayer("role-x"))
	if status != rsi.StatusOK {
		t.Errorf("delete layer after abort: status = %d, want StatusOK", status)
	}
}
