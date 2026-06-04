package handler

import (
	"path/filepath"
	"testing"

	"github.com/peios/loregd/internal/fold"
	"github.com/peios/loregd/internal/hivedb"
	"github.com/peios/loregd/internal/rsi"
)

func openTestHive(t *testing.T, name string) *hivedb.HiveDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".regdb")
	h, err := hivedb.Open(name, path)
	if err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func testHandler(t *testing.T) (*Handler, *hivedb.HiveDB) {
	t.Helper()
	hive := openTestHive(t, "Machine")
	h := New([]*hivedb.HiveDB{hive})
	return h, hive
}

func TestRegisterAllOperations(t *testing.T) {
	h, _ := testHandler(t)
	d := rsi.NewDispatcher()
	h.Register(d)

	ops := []uint16{
		rsi.OpLookup,
		rsi.OpCreateEntry,
		rsi.OpHideEntry,
		rsi.OpDeleteEntry,
		rsi.OpEnumChildren,
		rsi.OpCreateKey,
		rsi.OpReadKey,
		rsi.OpWriteKey,
		rsi.OpDropKey,
		rsi.OpQueryValues,
		rsi.OpSetValue,
		rsi.OpDeleteValueEntry,
		rsi.OpSetBlanketTombstone,
		rsi.OpBeginTransaction,
		rsi.OpCommitTransaction,
		rsi.OpAbortTransaction,
		rsi.OpDeleteLayer,
		rsi.OpFlush,
	}

	if got := d.HandlerCount(); got != len(ops) {
		t.Fatalf("registered handler count = %d, want %d", got, len(ops))
	}
	for _, op := range ops {
		if !d.HasHandler(op) {
			t.Errorf("op_code 0x%04X was not registered", op)
		}
	}
}

func insertKey(t *testing.T, hive *hivedb.HiveDB, guid rsi.GUID, name string, parentGUID rsi.GUID, volatile bool) {
	t.Helper()
	volInt := 0
	table := "main.keys"
	if volatile {
		volInt = 1
		table = "volatile.keys"
	}
	_, err := hive.WriteDB().Exec(`
		INSERT INTO `+table+` (guid, name, name_folded, parent_guid, sd, volatile, symlink, last_write_time)
		VALUES (?, ?, ?, ?, X'00', ?, 0, 0)
	`, guid[:], name, fold.String(name), parentGUID[:], volInt)
	if err != nil {
		t.Fatalf("insertKey(%s): %v", name, err)
	}
}

func insertPathEntry(t *testing.T, hive *hivedb.HiveDB, parentGUID rsi.GUID, childName, layer string, childGUID rsi.GUID, seq uint64) {
	t.Helper()
	_, err := hive.WriteDB().Exec(`
		INSERT INTO main.path_entries (parent_guid, child_name, child_name_folded, layer, target_type, target_guid, sequence)
		VALUES (?, ?, ?, ?, 0, ?, ?)
	`, parentGUID[:], childName, fold.String(childName), layer, childGUID[:], seq)
	if err != nil {
		t.Fatalf("insertPathEntry(%s): %v", childName, err)
	}
}

// --- RSI_LOOKUP tests ---

func TestLookupEmpty(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)

	status, payload := h.handleLookup(rsi.RequestHeader{}, encodeLookup(root, "nonexistent"))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d, want OK", status)
	}
	d := rsi.NewDecoder(payload)
	count, _ := d.Uint32()
	if count != 0 {
		t.Errorf("entry count = %d, want 0", count)
	}
}

func TestLookupFindsEntry(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	childGUID := rsi.GUID{0x01, 0x02, 0x03}

	insertKey(t, hive, childGUID, "System", root, false)
	insertPathEntry(t, hive, root, "System", "base", childGUID, 1)

	// Cache the child GUID so resolveHive works for it.
	h.guidCache.Store(childGUID, hive)

	status, payload := h.handleLookup(rsi.RequestHeader{}, encodeLookup(root, "System"))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	d := rsi.NewDecoder(payload)
	count, _ := d.Uint32()
	if count != 1 {
		t.Fatalf("entry count = %d, want 1", count)
	}

	layer, _ := d.String()
	if layer != "base" {
		t.Errorf("layer = %q, want %q", layer, "base")
	}
	tt, _ := d.Uint8()
	if tt != rsi.TargetGUID {
		t.Errorf("target_type = %d, want GUID", tt)
	}
	guid, _ := d.GUID()
	if guid != childGUID {
		t.Errorf("target_guid = %x, want %x", guid, childGUID)
	}
}

func TestLookupCaseInsensitive(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	childGUID := rsi.GUID{0x01}

	insertKey(t, hive, childGUID, "System", root, false)
	insertPathEntry(t, hive, root, "System", "base", childGUID, 1)

	// Query with different case.
	status, payload := h.handleLookup(rsi.RequestHeader{}, encodeLookup(root, "SYSTEM"))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}
	d := rsi.NewDecoder(payload)
	count, _ := d.Uint32()
	if count != 1 {
		t.Errorf("case-insensitive lookup failed: count = %d, want 1", count)
	}
}

// --- RSI_CREATE_KEY tests ---

func TestCreateKey(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	newGUID := rsi.GUID{0xAA, 0xBB}

	status, _ := h.handleCreateKey(rsi.RequestHeader{}, encodeCreateKey(newGUID, "NewKey", root, []byte{0x01}, false, false))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	// Verify key exists in database.
	var name string
	hive.WriteDB().QueryRow("SELECT name FROM main.keys WHERE guid = ?", newGUID[:]).Scan(&name)
	if name != "NewKey" {
		t.Errorf("key name = %q, want %q", name, "NewKey")
	}

	// Verify GUID is in cache.
	if _, ok := h.guidCache.Load(newGUID); !ok {
		t.Error("GUID not added to cache")
	}
}

func TestCreateKeyDuplicate(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0xCC}

	h.handleCreateKey(rsi.RequestHeader{}, encodeCreateKey(guid, "First", root, []byte{0x01}, false, false))
	status, _ := h.handleCreateKey(rsi.RequestHeader{}, encodeCreateKey(guid, "Second", root, []byte{0x01}, false, false))
	if status != rsi.StatusAlreadyExists {
		t.Errorf("duplicate create status = %d, want AlreadyExists", status)
	}
}

func TestCreateKeyVolatile(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0xDD}

	status, _ := h.handleCreateKey(rsi.RequestHeader{}, encodeCreateKey(guid, "VolKey", root, []byte{0x01}, true, false))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	// Should be in volatile database.
	var name string
	hive.WriteDB().QueryRow("SELECT name FROM volatile.keys WHERE guid = ?", guid[:]).Scan(&name)
	if name != "VolKey" {
		t.Errorf("volatile key name = %q, want %q", name, "VolKey")
	}
}

// --- RSI_READ_KEY tests ---

func TestReadKey(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0x10}

	insertKey(t, hive, guid, "TestKey", root, false)
	h.guidCache.Store(guid, hive)

	status, payload := h.handleReadKey(rsi.RequestHeader{}, encodeReadKey(guid))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	d := rsi.NewDecoder(payload)
	name, _ := d.String()
	if name != "TestKey" {
		t.Errorf("name = %q, want %q", name, "TestKey")
	}
}

func TestReadKeyNotFound(t *testing.T) {
	h, _ := testHandler(t)
	status, _ := h.handleReadKey(rsi.RequestHeader{}, encodeReadKey(rsi.GUID{0xFF}))
	if status != rsi.StatusNotFound {
		t.Errorf("status = %d, want NotFound", status)
	}
}

// --- RSI_WRITE_KEY tests ---

func TestWriteKeySD(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0x20}
	insertKey(t, hive, guid, "SDKey", root, false)
	h.guidCache.Store(guid, hive)

	newSD := []byte{0xAA, 0xBB, 0xCC}
	status, _ := h.handleWriteKey(rsi.RequestHeader{}, encodeWriteKey(guid, rsi.WriteKeyFieldSD, newSD, 0))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	var sd []byte
	hive.WriteDB().QueryRow("SELECT sd FROM main.keys WHERE guid = ?", guid[:]).Scan(&sd)
	if len(sd) != 3 || sd[0] != 0xAA {
		t.Errorf("sd = %x, want AABBCC", sd)
	}
}

func TestWriteKeyInvalidMask(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0x21}
	insertKey(t, hive, guid, "MaskKey", root, false)
	h.guidCache.Store(guid, hive)

	// field_mask 0x04 has bit 2 set — invalid.
	status, _ := h.handleWriteKey(rsi.RequestHeader{}, encodeWriteKey(guid, 0x04, nil, 0))
	if status != rsi.StatusInvalid {
		t.Errorf("status = %d, want Invalid", status)
	}
}

// --- RSI_DROP_KEY tests ---

func TestDropKey(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0x30}

	insertKey(t, hive, guid, "DropMe", root, false)
	insertPathEntry(t, hive, root, "DropMe", "base", guid, 1)

	// Also insert a value for the key.
	hive.WriteDB().Exec(`INSERT INTO main.[values] (key_guid, name, name_folded, layer, type, data, sequence) VALUES (?, 'v', 'v', 'base', 1, X'00', 1)`, guid[:])

	h.guidCache.Store(guid, hive)

	status, _ := h.handleDropKey(rsi.RequestHeader{}, encodeDropKey(guid))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	// Key should be gone.
	var count int
	hive.WriteDB().QueryRow("SELECT COUNT(*) FROM main.keys WHERE guid = ?", guid[:]).Scan(&count)
	if count != 0 {
		t.Error("key not deleted")
	}

	// Path entries should be gone.
	hive.WriteDB().QueryRow("SELECT COUNT(*) FROM main.path_entries WHERE target_guid = ?", guid[:]).Scan(&count)
	if count != 0 {
		t.Error("path entries not deleted")
	}

	// Values should be gone.
	hive.WriteDB().QueryRow("SELECT COUNT(*) FROM main.[values] WHERE key_guid = ?", guid[:]).Scan(&count)
	if count != 0 {
		t.Error("values not deleted")
	}

	// Cache should be cleared.
	if _, ok := h.guidCache.Load(guid); ok {
		t.Error("GUID still in cache after drop")
	}
}

func TestDropKeyIdempotent(t *testing.T) {
	h, _ := testHandler(t)
	status, _ := h.handleDropKey(rsi.RequestHeader{}, encodeDropKey(rsi.GUID{0xFF}))
	if status != rsi.StatusOK {
		t.Errorf("idempotent drop status = %d, want OK", status)
	}
}

// --- RSI_CREATE_ENTRY tests ---

func TestCreateEntry(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	childGUID := rsi.GUID{0x40}

	insertKey(t, hive, childGUID, "Child", root, false)
	h.guidCache.Store(childGUID, hive)

	status, _ := h.handleCreateEntry(rsi.RequestHeader{}, encodeCreateEntry(root, "Child", "base", childGUID, 10))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	var count int
	hive.WriteDB().QueryRow("SELECT COUNT(*) FROM main.path_entries WHERE parent_guid = ? AND child_name_folded = ?",
		root[:], "child").Scan(&count)
	if count != 1 {
		t.Errorf("path entry count = %d, want 1", count)
	}
}

func TestCreateEntryDuplicate(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	childGUID := rsi.GUID{0x41}

	insertKey(t, hive, childGUID, "Dup", root, false)
	h.guidCache.Store(childGUID, hive)

	h.handleCreateEntry(rsi.RequestHeader{}, encodeCreateEntry(root, "Dup", "base", childGUID, 1))
	status, _ := h.handleCreateEntry(rsi.RequestHeader{}, encodeCreateEntry(root, "Dup", "base", childGUID, 2))
	if status != rsi.StatusAlreadyExists {
		t.Errorf("duplicate status = %d, want AlreadyExists", status)
	}
}

// --- RSI_DELETE_ENTRY tests ---

func TestDeleteEntry(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	childGUID := rsi.GUID{0x50}

	insertKey(t, hive, childGUID, "Del", root, false)
	insertPathEntry(t, hive, root, "Del", "base", childGUID, 1)

	status, _ := h.handleDeleteEntry(rsi.RequestHeader{}, encodeDeleteEntry(root, "Del", "base"))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	var count int
	hive.WriteDB().QueryRow("SELECT COUNT(*) FROM main.path_entries WHERE parent_guid = ? AND child_name_folded = ?",
		root[:], "del").Scan(&count)
	if count != 0 {
		t.Error("path entry not deleted")
	}
}

func TestDeleteEntryIdempotent(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)

	status, _ := h.handleDeleteEntry(rsi.RequestHeader{}, encodeDeleteEntry(root, "nonexistent", "base"))
	if status != rsi.StatusOK {
		t.Errorf("idempotent delete status = %d, want OK", status)
	}
}

// --- RSI_ENUM_CHILDREN tests ---

func TestEnumChildrenEmpty(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)

	status, payload := h.handleEnumChildren(rsi.RequestHeader{}, encodeEnumChildren(root))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	d := rsi.NewDecoder(payload)
	count, _ := d.Uint32()
	if count != 0 {
		t.Errorf("child count = %d, want 0", count)
	}
}

func TestEnumChildrenMultiple(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid1 := rsi.GUID{0x60}
	guid2 := rsi.GUID{0x61}

	insertKey(t, hive, guid1, "Alpha", root, false)
	insertKey(t, hive, guid2, "Beta", root, false)
	insertPathEntry(t, hive, root, "Alpha", "base", guid1, 1)
	insertPathEntry(t, hive, root, "Beta", "base", guid2, 2)

	status, payload := h.handleEnumChildren(rsi.RequestHeader{}, encodeEnumChildren(root))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	d := rsi.NewDecoder(payload)
	count, _ := d.Uint32()
	if count != 2 {
		t.Errorf("child count = %d, want 2", count)
	}
}

// --- RSI_HIDE_ENTRY tests ---

func TestHideEntry(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)

	status, _ := h.handleHideEntry(rsi.RequestHeader{}, encodeHideEntry(root, "HiddenChild", "role-x", 5))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	// Verify HIDDEN entry exists.
	var targetType int
	var seq uint64
	hive.WriteDB().QueryRow(`
		SELECT target_type, sequence FROM main.path_entries
		WHERE parent_guid = ? AND child_name_folded = ? AND layer = ?
	`, root[:], "hiddenchild", "role-x").Scan(&targetType, &seq)
	if targetType != int(rsi.TargetHidden) {
		t.Errorf("target_type = %d, want %d (HIDDEN)", targetType, rsi.TargetHidden)
	}
	if seq != 5 {
		t.Errorf("sequence = %d, want 5", seq)
	}
}

func TestHideEntryReplacesExisting(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	childGUID := rsi.GUID{0x70}

	// Create a normal path entry first.
	insertKey(t, hive, childGUID, "Target", root, false)
	insertPathEntry(t, hive, root, "Target", "layer1", childGUID, 1)

	// Hide it in the same layer — should replace.
	status, _ := h.handleHideEntry(rsi.RequestHeader{}, encodeHideEntry(root, "Target", "layer1", 10))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	var targetType int
	hive.WriteDB().QueryRow(`
		SELECT target_type FROM main.path_entries
		WHERE parent_guid = ? AND child_name_folded = ? AND layer = ?
	`, root[:], "target", "layer1").Scan(&targetType)
	if targetType != int(rsi.TargetHidden) {
		t.Errorf("after hide: target_type = %d, want HIDDEN", targetType)
	}
}

// --- RSI_CREATE_ENTRY additional tests ---

func TestCreateEntryVolatileTarget(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	childGUID := rsi.GUID{0x42}

	// Create a volatile key.
	insertKey(t, hive, childGUID, "VolChild", root, true)
	h.guidCache.Store(childGUID, hive)

	status, _ := h.handleCreateEntry(rsi.RequestHeader{}, encodeCreateEntry(root, "VolChild", "base", childGUID, 10))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	// Path entry should be in volatile database.
	var count int
	hive.WriteDB().QueryRow("SELECT COUNT(*) FROM volatile.path_entries WHERE parent_guid = ? AND child_name_folded = ?",
		root[:], "volchild").Scan(&count)
	if count != 1 {
		t.Errorf("volatile path entry count = %d, want 1", count)
	}

	// Should NOT be in main database.
	hive.WriteDB().QueryRow("SELECT COUNT(*) FROM main.path_entries WHERE parent_guid = ? AND child_name_folded = ?",
		root[:], "volchild").Scan(&count)
	if count != 0 {
		t.Errorf("main path entry count = %d, want 0", count)
	}
}

// --- RSI_HIDE_ENTRY additional tests ---

func TestHideEntryVolatileParent(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	parentGUID := rsi.GUID{0x71}

	// Create a volatile parent key.
	insertKey(t, hive, parentGUID, "VolParent", root, true)
	h.guidCache.Store(parentGUID, hive)

	status, _ := h.handleHideEntry(rsi.RequestHeader{}, encodeHideEntry(parentGUID, "HiddenChild", "layer1", 5))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	// HIDDEN entry should be in volatile database.
	var targetType int
	err := hive.WriteDB().QueryRow(`
		SELECT target_type FROM volatile.path_entries
		WHERE parent_guid = ? AND child_name_folded = ? AND layer = ?
	`, parentGUID[:], "hiddenchild", "layer1").Scan(&targetType)
	if err != nil {
		t.Fatalf("query volatile HIDDEN entry: %v", err)
	}
	if targetType != int(rsi.TargetHidden) {
		t.Errorf("target_type = %d, want HIDDEN", targetType)
	}
}

// --- RSI_CREATE_KEY additional tests ---

func TestCreateKeyUnknownParent(t *testing.T) {
	h, _ := testHandler(t)
	unknownParent := rsi.GUID{0xFE, 0xED}
	newGUID := rsi.GUID{0xAA}

	status, _ := h.handleCreateKey(rsi.RequestHeader{}, encodeCreateKey(newGUID, "Orphan", unknownParent, []byte{0x01}, false, false))
	if status != rsi.StatusNotFound {
		t.Errorf("status = %d, want NotFound", status)
	}
}

// --- RSI_WRITE_KEY additional tests ---

func TestWriteKeyNoOpMask(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0x80}
	insertKey(t, hive, guid, "NoOpKey", root, false)
	h.guidCache.Store(guid, hive)

	// field_mask 0 = no-op. Should succeed without changing anything.
	status, _ := h.handleWriteKey(rsi.RequestHeader{}, encodeWriteKey(guid, 0, nil, 0))
	if status != rsi.StatusOK {
		t.Errorf("no-op write status = %d, want OK", status)
	}
}

func TestWriteKeyLastWriteTime(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0x81}
	insertKey(t, hive, guid, "LWTKey", root, false)
	h.guidCache.Store(guid, hive)

	var lwt int64 = 1234567890
	status, _ := h.handleWriteKey(rsi.RequestHeader{}, encodeWriteKey(guid, rsi.WriteKeyFieldLastWriteTime, nil, lwt))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	var got int64
	hive.WriteDB().QueryRow("SELECT last_write_time FROM main.keys WHERE guid = ?", guid[:]).Scan(&got)
	if got != lwt {
		t.Errorf("last_write_time = %d, want %d", got, lwt)
	}
}

func TestWriteKeyBothFields(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0x82}
	insertKey(t, hive, guid, "BothKey", root, false)
	h.guidCache.Store(guid, hive)

	newSD := []byte{0xDD, 0xEE}
	var lwt int64 = 9999
	status, _ := h.handleWriteKey(rsi.RequestHeader{}, encodeWriteKey(guid, rsi.WriteKeyFieldSD|rsi.WriteKeyFieldLastWriteTime, newSD, lwt))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	var sd []byte
	var got int64
	hive.WriteDB().QueryRow("SELECT sd, last_write_time FROM main.keys WHERE guid = ?", guid[:]).Scan(&sd, &got)
	if len(sd) != 2 || sd[0] != 0xDD {
		t.Errorf("sd = %x, want DDEE", sd)
	}
	if got != lwt {
		t.Errorf("last_write_time = %d, want %d", got, lwt)
	}
}

func TestWriteKeyNotFound(t *testing.T) {
	h, _ := testHandler(t)
	// GUID that doesn't exist anywhere.
	status, _ := h.handleWriteKey(rsi.RequestHeader{}, encodeWriteKey(rsi.GUID{0xFE}, rsi.WriteKeyFieldSD, []byte{0x01}, 0))
	if status != rsi.StatusNotFound {
		t.Errorf("status = %d, want NotFound", status)
	}
}

func TestWriteKeyNotFoundWithNoOpMask(t *testing.T) {
	h, hive := testHandler(t)
	// GUID that is in cache but was dropped from DB.
	guid := rsi.GUID{0x83}
	h.guidCache.Store(guid, hive)

	status, _ := h.handleWriteKey(rsi.RequestHeader{}, encodeWriteKey(guid, 0, nil, 0))
	if status != rsi.StatusNotFound {
		t.Errorf("status = %d, want NotFound for stale cache entry", status)
	}
}

// --- RSI_DROP_KEY additional tests ---

func TestDropKeyInTransaction(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0x31}

	insertKey(t, hive, guid, "TxnDrop", root, false)
	insertPathEntry(t, hive, root, "TxnDrop", "base", guid, 1)
	h.guidCache.Store(guid, hive)

	// Begin transaction.
	h.txns.begin(50)

	// Drop within transaction.
	hdr := rsi.RequestHeader{TxnID: 50}
	status, _ := h.handleDropKey(hdr, encodeDropKey(guid))
	if status != rsi.StatusOK {
		t.Fatalf("drop in txn: status = %d", status)
	}

	// Commit.
	h.txns.commit(50)

	// Key should be gone.
	var count int
	hive.WriteDB().QueryRow("SELECT COUNT(*) FROM main.keys WHERE guid = ?", guid[:]).Scan(&count)
	if count != 0 {
		t.Error("key not deleted after transactional drop")
	}
}

// --- Payload builders ---

func encodeLookup(parentGUID rsi.GUID, childName string) []byte {
	e := rsi.NewEncoder(64)
	e.PutGUID(parentGUID)
	e.PutString(childName)
	return e.Bytes()
}

func encodeCreateKey(guid rsi.GUID, name string, parentGUID rsi.GUID, sd []byte, volatile, symlink bool) []byte {
	e := rsi.NewEncoder(128)
	e.PutGUID(guid)
	e.PutString(name)
	e.PutGUID(parentGUID)
	e.PutBlob(sd)
	e.PutUint8(boolByte(volatile))
	e.PutUint8(boolByte(symlink))
	return e.Bytes()
}

func encodeReadKey(guid rsi.GUID) []byte {
	e := rsi.NewEncoder(16)
	e.PutGUID(guid)
	return e.Bytes()
}

func encodeWriteKey(guid rsi.GUID, fieldMask uint32, sd []byte, lastWriteTime int64) []byte {
	e := rsi.NewEncoder(64)
	e.PutGUID(guid)
	e.PutUint32(fieldMask)
	if fieldMask&rsi.WriteKeyFieldSD != 0 {
		e.PutBlob(sd)
	}
	if fieldMask&rsi.WriteKeyFieldLastWriteTime != 0 {
		e.PutUint64(uint64(lastWriteTime))
	}
	return e.Bytes()
}

func encodeDropKey(guid rsi.GUID) []byte {
	e := rsi.NewEncoder(16)
	e.PutGUID(guid)
	return e.Bytes()
}

func encodeCreateEntry(parentGUID rsi.GUID, childName, layer string, childGUID rsi.GUID, seq uint64) []byte {
	e := rsi.NewEncoder(128)
	e.PutGUID(parentGUID)
	e.PutString(childName)
	e.PutString(layer)
	e.PutGUID(childGUID)
	e.PutUint64(seq)
	return e.Bytes()
}

func encodeHideEntry(parentGUID rsi.GUID, childName, layer string, seq uint64) []byte {
	e := rsi.NewEncoder(64)
	e.PutGUID(parentGUID)
	e.PutString(childName)
	e.PutString(layer)
	e.PutUint64(seq)
	return e.Bytes()
}

func encodeDeleteEntry(parentGUID rsi.GUID, childName, layer string) []byte {
	e := rsi.NewEncoder(64)
	e.PutGUID(parentGUID)
	e.PutString(childName)
	e.PutString(layer)
	return e.Bytes()
}

func encodeEnumChildren(parentGUID rsi.GUID) []byte {
	e := rsi.NewEncoder(16)
	e.PutGUID(parentGUID)
	return e.Bytes()
}

func boolByte(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}
