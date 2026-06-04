package handler

import (
	"testing"

	"github.com/peios/loregd/internal/fold"
	"github.com/peios/loregd/internal/hivedb"
	"github.com/peios/loregd/internal/rsi"
)

// --- RSI_BEGIN/COMMIT/ABORT tests ---

func TestBeginTransaction(t *testing.T) {
	h, _ := testHandler(t)
	status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxn(1))
	if status != rsi.StatusOK {
		t.Fatalf("begin: status = %d", status)
	}
}

func TestCommitUnboundTransaction(t *testing.T) {
	h, _ := testHandler(t)
	h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxn(1))
	// Commit without any operations (never bound). Should succeed.
	status, _ := h.handleCommitTransaction(rsi.RequestHeader{}, encodeCommitTxn(1))
	if status != rsi.StatusOK {
		t.Errorf("commit unbound: status = %d", status)
	}
}

func TestTransactionWriteAndCommit(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0x01, 0x01}

	// Begin transaction.
	h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxn(42))

	// Create a key within the transaction.
	hdr := rsi.RequestHeader{TxnID: 42}
	status, _ := h.handleCreateKey(hdr, encodeCreateKey(guid, "TxnKey", root, []byte{0x01}, false, false))
	if status != rsi.StatusOK {
		t.Fatalf("create key in txn: status = %d", status)
	}

	// Read within the transaction should see the uncommitted key.
	status, payload := h.handleReadKey(hdr, encodeReadKey(guid))
	if status != rsi.StatusOK {
		t.Fatalf("read in txn: status = %d (should see uncommitted write)", status)
	}
	d := rsi.NewDecoder(payload)
	name, _ := d.String()
	if name != "TxnKey" {
		t.Errorf("read name = %q, want %q", name, "TxnKey")
	}

	// Commit.
	status, _ = h.handleCommitTransaction(rsi.RequestHeader{}, encodeCommitTxn(42))
	if status != rsi.StatusOK {
		t.Fatalf("commit: status = %d", status)
	}

	// Key should be visible via normal (non-transactional) read.
	status, _ = h.handleReadKey(rsi.RequestHeader{}, encodeReadKey(guid))
	if status != rsi.StatusOK {
		t.Errorf("post-commit read: status = %d", status)
	}
}

func TestTransactionAbort(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0xA1}

	h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxn(99))

	// Create a key within the transaction.
	hdr := rsi.RequestHeader{TxnID: 99}
	status, _ := h.handleCreateKey(hdr, encodeCreateKey(guid, "AbortMe", root, []byte{0x01}, false, false))
	if status != rsi.StatusOK {
		t.Fatalf("create in txn: status = %d", status)
	}

	// Abort.
	status, _ = h.handleAbortTransaction(rsi.RequestHeader{}, encodeAbortTxn(99))
	if status != rsi.StatusOK {
		t.Fatalf("abort: status = %d", status)
	}

	// Key should NOT be visible.
	var count int
	hive.WriteDB().QueryRow("SELECT COUNT(*) FROM main.keys WHERE guid = ?", guid[:]).Scan(&count)
	if count != 0 {
		t.Error("aborted key is still visible")
	}
}

func TestTransactionIsolation(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0xB1}

	h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxn(7))

	// Create key in transaction.
	hdr := rsi.RequestHeader{TxnID: 7}
	h.handleCreateKey(hdr, encodeCreateKey(guid, "IsoKey", root, []byte{0x01}, false, false))

	// Non-transactional read should NOT see the uncommitted key.
	status, _ := h.handleReadKey(rsi.RequestHeader{}, encodeReadKey(guid))
	if status != rsi.StatusNotFound {
		t.Errorf("isolation: non-txn read status = %d, want NotFound", status)
	}

	h.handleCommitTransaction(rsi.RequestHeader{}, encodeCommitTxn(7))
}

func TestBeginTransactionDuplicate(t *testing.T) {
	h, _ := testHandler(t)
	status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxn(1))
	if status != rsi.StatusOK {
		t.Fatalf("first begin: status = %d", status)
	}

	// Duplicate begin should fail.
	status, _ = h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxn(1))
	if status != rsi.StatusInvalid {
		t.Errorf("duplicate begin: status = %d, want Invalid", status)
	}
}

func TestAbortUnknownTransaction(t *testing.T) {
	h, _ := testHandler(t)

	// Aborting a transaction that was never started should still succeed
	// (spec: "Always succeeds").
	status, _ := h.handleAbortTransaction(rsi.RequestHeader{}, encodeAbortTxn(999))
	if status != rsi.StatusOK {
		t.Errorf("abort unknown: status = %d, want OK", status)
	}
}

func TestAbortAlreadyAborted(t *testing.T) {
	h, _ := testHandler(t)
	h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxn(10))
	h.handleAbortTransaction(rsi.RequestHeader{}, encodeAbortTxn(10))

	// Aborting again should still succeed.
	status, _ := h.handleAbortTransaction(rsi.RequestHeader{}, encodeAbortTxn(10))
	if status != rsi.StatusOK {
		t.Errorf("double abort: status = %d, want OK", status)
	}
}

func TestCommitPreservesStateOnFailure(t *testing.T) {
	h, _ := testHandler(t)
	// Commit an unknown transaction — should fail.
	status, _ := h.handleCommitTransaction(rsi.RequestHeader{}, encodeCommitTxn(777))
	if status != rsi.StatusStorageError {
		t.Errorf("commit unknown: status = %d, want StorageError", status)
	}
}

func TestTransactionHiveConsistency(t *testing.T) {
	// Test that a transaction bound to one hive rejects operations
	// targeting a different hive.
	hive1 := openTestHive(t, "Machine")
	hive2 := openTestHive(t, "Users")
	h := New([]*hivedb.HiveDB{hive1, hive2})

	root1 := rsi.GUID(hive1.RootGUID)
	root2 := rsi.GUID(hive2.RootGUID)
	guid1 := rsi.GUID{0xE1}
	guid2 := rsi.GUID{0xE2}

	insertKey(t, hive1, guid1, "Key1", root1, false)
	insertKey(t, hive2, guid2, "Key2", root2, false)
	h.guidCache.Store(guid1, hive1)
	h.guidCache.Store(guid2, hive2)

	// Begin transaction, bind to hive1 via a write.
	h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxn(30))
	hdr := rsi.RequestHeader{TxnID: 30}
	status, _ := h.handleWriteKey(hdr, encodeWriteKey(guid1, rsi.WriteKeyFieldSD, []byte{0x01}, 0))
	if status != rsi.StatusOK {
		t.Fatalf("write to hive1: status = %d", status)
	}

	// Now try to write to hive2 in the same transaction — should fail.
	status, _ = h.handleWriteKey(hdr, encodeWriteKey(guid2, rsi.WriteKeyFieldSD, []byte{0x02}, 0))
	if status == rsi.StatusOK {
		t.Error("cross-hive write should have failed, got StatusOK")
	}

	h.txns.abort(30)
}

// --- RSI_DELETE_LAYER tests ---

func TestDeleteLayerEmpty(t *testing.T) {
	h, _ := testHandler(t)
	status, payload := h.handleDeleteLayer(rsi.RequestHeader{}, encodeDeleteLayer("nonexistent"))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}
	d := rsi.NewDecoder(payload)
	count, _ := d.Uint32()
	if count != 0 {
		t.Errorf("orphan count = %d, want 0", count)
	}
}

func TestDeleteLayerPurgesEntries(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0xC1}

	// Create a key with path entry and value in "role-x" layer.
	insertKey(t, hive, guid, "LayerChild", root, false)
	insertPathEntry(t, hive, root, "LayerChild", "role-x", guid, 1)

	_, err := hive.WriteDB().Exec(`
		INSERT INTO main.[values] (key_guid, name, name_folded, layer, type, data, sequence)
		VALUES (?, 'setting', 'setting', 'role-x', 1, X'FF', 2)
	`, guid[:])
	if err != nil {
		t.Fatal(err)
	}

	// Also add a base layer entry so the key is NOT orphaned.
	insertPathEntry(t, hive, root, "LayerChild", "base", guid, 0)

	status, payload := h.handleDeleteLayer(rsi.RequestHeader{}, encodeDeleteLayer("role-x"))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	// The role-x path entry and value should be gone.
	var peCount, vCount int
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM main.path_entries WHERE layer = 'role-x'`).Scan(&peCount)
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM main.[values] WHERE layer = 'role-x'`).Scan(&vCount)
	if peCount != 0 {
		t.Errorf("path entries for role-x = %d, want 0", peCount)
	}
	if vCount != 0 {
		t.Errorf("values for role-x = %d, want 0", vCount)
	}

	// The base layer entry should survive.
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM main.path_entries WHERE layer = 'base' AND child_name_folded = ?`,
		fold.String("LayerChild")).Scan(&peCount)
	if peCount != 1 {
		t.Errorf("base layer path entries = %d, want 1", peCount)
	}

	// No orphans (base layer entry still references the key).
	d := rsi.NewDecoder(payload)
	orphanCount, _ := d.Uint32()
	if orphanCount != 0 {
		t.Errorf("orphan count = %d, want 0", orphanCount)
	}
}

func TestDeleteLayerReportsOrphans(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0xD1}

	// Key with path entry ONLY in the layer being deleted → orphaned.
	insertKey(t, hive, guid, "OrphanChild", root, false)
	insertPathEntry(t, hive, root, "OrphanChild", "role-y", guid, 1)

	status, payload := h.handleDeleteLayer(rsi.RequestHeader{}, encodeDeleteLayer("role-y"))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	d := rsi.NewDecoder(payload)
	orphanCount, _ := d.Uint32()
	if orphanCount != 1 {
		t.Fatalf("orphan count = %d, want 1", orphanCount)
	}
	orphanGUID, _ := d.GUID()
	if orphanGUID != guid {
		t.Errorf("orphan GUID = %x, want %x", orphanGUID, guid)
	}
}

func TestDeleteLayerPurgesBlanketTombstones(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0xC2}

	insertKey(t, hive, guid, "BTChild", root, false)
	insertPathEntry(t, hive, root, "BTChild", "role-z", guid, 1)
	// Also add a base layer entry so the key is NOT orphaned.
	insertPathEntry(t, hive, root, "BTChild", "base", guid, 0)

	// Add a blanket tombstone in the layer being deleted.
	hive.WriteDB().Exec(`INSERT INTO main.blanket_tombstones (key_guid, layer, sequence) VALUES (?, 'role-z', 50)`, guid[:])

	status, _ := h.handleDeleteLayer(rsi.RequestHeader{}, encodeDeleteLayer("role-z"))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	// Blanket tombstone for role-z should be gone.
	var btCount int
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM main.blanket_tombstones WHERE layer = 'role-z'`).Scan(&btCount)
	if btCount != 0 {
		t.Errorf("blanket_tombstones for role-z = %d, want 0", btCount)
	}
}

func TestDeleteLayerVolatileEntries(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0xC3}

	// Create a volatile key with path entry and value in "role-v" layer.
	insertKey(t, hive, guid, "VolLayerChild", root, true)

	// Insert volatile path entry.
	_, err := hive.WriteDB().Exec(`
		INSERT INTO volatile.path_entries (parent_guid, child_name, child_name_folded, layer, target_type, target_guid, sequence)
		VALUES (?, 'VolLayerChild', ?, 'role-v', 0, ?, 1)
	`, root[:], fold.String("VolLayerChild"), guid[:])
	if err != nil {
		t.Fatal(err)
	}

	// Insert volatile value.
	_, err = hive.WriteDB().Exec(`
		INSERT INTO volatile.[values] (key_guid, name, name_folded, layer, type, data, sequence)
		VALUES (?, 'setting', 'setting', 'role-v', 1, X'FF', 2)
	`, guid[:])
	if err != nil {
		t.Fatal(err)
	}

	// Insert volatile blanket tombstone.
	_, err = hive.WriteDB().Exec(`
		INSERT INTO volatile.blanket_tombstones (key_guid, layer, sequence)
		VALUES (?, 'role-v', 3)
	`, guid[:])
	if err != nil {
		t.Fatal(err)
	}

	// Also add a base layer entry so the key is not orphaned.
	_, err = hive.WriteDB().Exec(`
		INSERT INTO volatile.path_entries (parent_guid, child_name, child_name_folded, layer, target_type, target_guid, sequence)
		VALUES (?, 'VolLayerChild', ?, 'base', 0, ?, 0)
	`, root[:], fold.String("VolLayerChild"), guid[:])
	if err != nil {
		t.Fatal(err)
	}

	status, payload := h.handleDeleteLayer(rsi.RequestHeader{}, encodeDeleteLayer("role-v"))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	// All volatile entries for role-v should be gone.
	var peCount, vCount, btCount int
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM volatile.path_entries WHERE layer = 'role-v'`).Scan(&peCount)
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM volatile.[values] WHERE layer = 'role-v'`).Scan(&vCount)
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM volatile.blanket_tombstones WHERE layer = 'role-v'`).Scan(&btCount)
	if peCount != 0 {
		t.Errorf("volatile path_entries for role-v = %d, want 0", peCount)
	}
	if vCount != 0 {
		t.Errorf("volatile values for role-v = %d, want 0", vCount)
	}
	if btCount != 0 {
		t.Errorf("volatile blanket_tombstones for role-v = %d, want 0", btCount)
	}

	// Base layer entry should survive.
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM volatile.path_entries WHERE layer = 'base' AND child_name_folded = ?`,
		fold.String("VolLayerChild")).Scan(&peCount)
	if peCount != 1 {
		t.Errorf("base layer volatile path entries = %d, want 1", peCount)
	}

	// No orphans (base layer entry still references the key).
	d := rsi.NewDecoder(payload)
	orphanCount, _ := d.Uint32()
	if orphanCount != 0 {
		t.Errorf("orphan count = %d, want 0", orphanCount)
	}
}

// --- RSI_FLUSH tests ---

func TestFlush(t *testing.T) {
	h, _ := testHandler(t)
	status, _ := h.handleFlush(rsi.RequestHeader{}, encodeFlush("Machine"))
	if status != rsi.StatusOK {
		t.Errorf("flush: status = %d", status)
	}
}

func TestFlushInvalidHive(t *testing.T) {
	h, _ := testHandler(t)
	status, _ := h.handleFlush(rsi.RequestHeader{}, encodeFlush("NonExistent"))
	if status != rsi.StatusInvalid {
		t.Errorf("flush bad hive: status = %d, want Invalid", status)
	}
}

func TestFlushCaseInsensitive(t *testing.T) {
	h, _ := testHandler(t)
	status, _ := h.handleFlush(rsi.RequestHeader{}, encodeFlush("MACHINE"))
	if status != rsi.StatusOK {
		t.Errorf("flush case: status = %d", status)
	}
}

// --- Payload builders ---

func encodeBeginTxn(txnID uint64) []byte {
	e := rsi.NewEncoder(8)
	e.PutUint64(txnID)
	return e.Bytes()
}

func encodeCommitTxn(txnID uint64) []byte {
	e := rsi.NewEncoder(8)
	e.PutUint64(txnID)
	return e.Bytes()
}

func encodeAbortTxn(txnID uint64) []byte {
	e := rsi.NewEncoder(8)
	e.PutUint64(txnID)
	return e.Bytes()
}

func encodeDeleteLayer(layerName string) []byte {
	e := rsi.NewEncoder(32)
	e.PutString(layerName)
	return e.Bytes()
}

func encodeFlush(hiveName string) []byte {
	e := rsi.NewEncoder(32)
	e.PutString(hiveName)
	return e.Bytes()
}
