package handler

import (
	"testing"

	"github.com/peios/loregd/internal/fold"
	"github.com/peios/loregd/internal/hivedb"
	"github.com/peios/loregd/internal/rsi"
)

func setupKeyForValues(t *testing.T) (*Handler, *hivedb.HiveDB, rsi.GUID) {
	t.Helper()
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0xA0}
	insertKey(t, hive, guid, "ValKey", root, false)
	h.guidCache.Store(guid, hive)
	return h, hive, guid
}

func insertValue(t *testing.T, hive *hivedb.HiveDB, keyGUID rsi.GUID, name, layer string, typ uint32, data []byte, seq uint64) {
	t.Helper()
	_, err := hive.WriteDB().Exec(`
		INSERT INTO main.[values] (key_guid, name, name_folded, layer, type, data, sequence)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, keyGUID[:], name, fold.String(name), layer, typ, data, seq)
	if err != nil {
		t.Fatalf("insertValue(%s): %v", name, err)
	}
}

// --- RSI_QUERY_VALUES tests ---

func TestQueryValuesSingle(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	insertValue(t, hive, guid, "Name", "base", 1, []byte("hello"), 10)

	status, payload := h.handleQueryValues(rsi.RequestHeader{}, encodeQueryValues(guid, "Name", false))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	d := rsi.NewDecoder(payload)
	count, _ := d.Uint32()
	if count != 1 {
		t.Fatalf("entry count = %d, want 1", count)
	}
	vn, _ := d.String()
	if vn != "Name" {
		t.Errorf("value name = %q", vn)
	}
	ln, _ := d.String()
	if ln != "base" {
		t.Errorf("layer = %q", ln)
	}
	vt, _ := d.Uint32()
	if vt != 1 {
		t.Errorf("type = %d", vt)
	}
	data, _ := d.Blob()
	if string(data) != "hello" {
		t.Errorf("data = %q", data)
	}
	seq, _ := d.Uint64()
	if seq != 10 {
		t.Errorf("sequence = %d", seq)
	}

	// Blanket count should be 0.
	bc, _ := d.Uint32()
	if bc != 0 {
		t.Errorf("blanket count = %d, want 0", bc)
	}
}

func TestQueryValuesCaseInsensitive(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	insertValue(t, hive, guid, "MyValue", "base", 1, []byte("x"), 1)

	status, payload := h.handleQueryValues(rsi.RequestHeader{}, encodeQueryValues(guid, "MYVALUE", false))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	d := rsi.NewDecoder(payload)
	count, _ := d.Uint32()
	if count != 1 {
		t.Errorf("case-insensitive query: count = %d, want 1", count)
	}
}

func TestQueryValuesAll(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	insertValue(t, hive, guid, "A", "base", 1, []byte("a"), 1)
	insertValue(t, hive, guid, "B", "base", 4, []byte{0x01, 0x00, 0x00, 0x00}, 2)
	insertValue(t, hive, guid, "A", "role-x", 1, []byte("override"), 3)

	status, payload := h.handleQueryValues(rsi.RequestHeader{}, encodeQueryValues(guid, "", true))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	d := rsi.NewDecoder(payload)
	count, _ := d.Uint32()
	if count != 3 {
		t.Errorf("query-all count = %d, want 3", count)
	}
}

func TestQueryValuesEmpty(t *testing.T) {
	h, _, guid := setupKeyForValues(t)

	status, payload := h.handleQueryValues(rsi.RequestHeader{}, encodeQueryValues(guid, "nonexistent", false))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	d := rsi.NewDecoder(payload)
	count, _ := d.Uint32()
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestQueryValuesWithBlanketTombstone(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	insertValue(t, hive, guid, "V", "base", 1, []byte("v"), 1)

	// Add a blanket tombstone.
	hive.WriteDB().Exec(`INSERT INTO main.blanket_tombstones (key_guid, layer, sequence) VALUES (?, 'gpo-1', 20)`, guid[:])

	status, payload := h.handleQueryValues(rsi.RequestHeader{}, encodeQueryValues(guid, "V", false))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	d := rsi.NewDecoder(payload)
	entryCount, _ := d.Uint32()
	if entryCount != 1 {
		t.Fatalf("entry count = %d", entryCount)
	}
	// Skip value entry fields.
	d.String() // name
	d.String() // layer
	d.Uint32() // type
	d.Blob()   // data
	d.Uint64() // sequence

	blanketCount, _ := d.Uint32()
	if blanketCount != 1 {
		t.Fatalf("blanket count = %d, want 1", blanketCount)
	}
	bl, _ := d.String()
	if bl != "gpo-1" {
		t.Errorf("blanket layer = %q", bl)
	}
	bs, _ := d.Uint64()
	if bs != 20 {
		t.Errorf("blanket sequence = %d", bs)
	}
}

func TestQueryValuesKeyNotFound(t *testing.T) {
	h, _, _ := setupKeyForValues(t)
	status, _ := h.handleQueryValues(rsi.RequestHeader{}, encodeQueryValues(rsi.GUID{0xFF}, "x", false))
	if status != rsi.StatusNotFound {
		t.Errorf("status = %d, want NotFound", status)
	}
}

// --- RSI_SET_VALUE tests ---

func TestSetValue(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)

	status, _ := h.handleSetValue(rsi.RequestHeader{}, encodeSetValue(guid, "NewVal", "base", 1, []byte("data"), 5, 0))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	var name string
	var typ uint32
	hive.WriteDB().QueryRow(`SELECT name, type FROM main.[values] WHERE key_guid = ? AND name_folded = ?`,
		guid[:], "newval").Scan(&name, &typ)
	if name != "NewVal" {
		t.Errorf("stored name = %q, want %q (case-preserving)", name, "NewVal")
	}
	if typ != 1 {
		t.Errorf("stored type = %d", typ)
	}
}

func TestSetValueReplace(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	insertValue(t, hive, guid, "Replace", "base", 1, []byte("old"), 1)

	status, _ := h.handleSetValue(rsi.RequestHeader{}, encodeSetValue(guid, "Replace", "base", 1, []byte("new"), 2, 0))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	var data []byte
	hive.WriteDB().QueryRow(`SELECT data FROM main.[values] WHERE key_guid = ? AND name_folded = ? AND layer = ?`,
		guid[:], "replace", "base").Scan(&data)
	if string(data) != "new" {
		t.Errorf("data = %q, want %q", data, "new")
	}
}

func TestSetValueTombstone(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)

	// REG_TOMBSTONE = 0xFFFF
	status, _ := h.handleSetValue(rsi.RequestHeader{}, encodeSetValue(guid, "Dead", "base", 0xFFFF, nil, 1, 0))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	var typ uint32
	hive.WriteDB().QueryRow(`SELECT type FROM main.[values] WHERE key_guid = ? AND name_folded = ?`,
		guid[:], "dead").Scan(&typ)
	if typ != 0xFFFF {
		t.Errorf("type = %d, want 0xFFFF (REG_TOMBSTONE)", typ)
	}
}

func TestSetValueCASSuccess(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	insertValue(t, hive, guid, "CAS", "base", 1, []byte("old"), 10)

	// CAS with expected_sequence=10 should succeed.
	status, _ := h.handleSetValue(rsi.RequestHeader{}, encodeSetValue(guid, "CAS", "base", 1, []byte("new"), 20, 10))
	if status != rsi.StatusOK {
		t.Fatalf("CAS success: status = %d", status)
	}

	var data []byte
	hive.WriteDB().QueryRow(`SELECT data FROM main.[values] WHERE key_guid = ? AND name_folded = ? AND layer = ?`,
		guid[:], "cas", "base").Scan(&data)
	if string(data) != "new" {
		t.Errorf("CAS updated data = %q, want %q", data, "new")
	}
}

func TestSetValueCASMismatch(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	insertValue(t, hive, guid, "CAS", "base", 1, []byte("old"), 10)

	// CAS with expected_sequence=99 should fail.
	status, _ := h.handleSetValue(rsi.RequestHeader{}, encodeSetValue(guid, "CAS", "base", 1, []byte("new"), 20, 99))
	if status != rsi.StatusCASFailed {
		t.Errorf("CAS mismatch: status = %d, want CASFailed", status)
	}

	// Value should be unchanged.
	var data []byte
	hive.WriteDB().QueryRow(`SELECT data FROM main.[values] WHERE key_guid = ? AND name_folded = ? AND layer = ?`,
		guid[:], "cas", "base").Scan(&data)
	if string(data) != "old" {
		t.Errorf("value changed after CAS failure: %q", data)
	}
}

func TestSetValueCASNonExistent(t *testing.T) {
	h, _, guid := setupKeyForValues(t)

	// CAS on a non-existent entry should fail.
	status, _ := h.handleSetValue(rsi.RequestHeader{}, encodeSetValue(guid, "NoSuch", "base", 1, []byte("x"), 1, 5))
	if status != rsi.StatusCASFailed {
		t.Errorf("CAS non-existent: status = %d, want CASFailed", status)
	}
}

func TestSetValueKeyNotFound(t *testing.T) {
	h, _, _ := setupKeyForValues(t)
	status, _ := h.handleSetValue(rsi.RequestHeader{}, encodeSetValue(rsi.GUID{0xFF}, "x", "base", 1, nil, 1, 0))
	if status != rsi.StatusNotFound {
		t.Errorf("status = %d, want NotFound", status)
	}
}

func TestSetValueVolatileKey(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0xA1}

	// Create a volatile key.
	insertKey(t, hive, guid, "VolValKey", root, true)
	h.guidCache.Store(guid, hive)

	status, _ := h.handleSetValue(rsi.RequestHeader{}, encodeSetValue(guid, "VolVal", "base", 1, []byte("vdata"), 5, 0))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	// Value should be in volatile database.
	var name string
	hive.WriteDB().QueryRow(`SELECT name FROM volatile.[values] WHERE key_guid = ? AND name_folded = ?`,
		guid[:], "volval").Scan(&name)
	if name != "VolVal" {
		t.Errorf("volatile value name = %q, want %q", name, "VolVal")
	}

	// Should NOT be in main database.
	var count int
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM main.[values] WHERE key_guid = ? AND name_folded = ?`,
		guid[:], "volval").Scan(&count)
	if count != 0 {
		t.Errorf("main value count = %d, want 0", count)
	}
}

func TestSetValueCASInTransaction(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	insertValue(t, hive, guid, "TxnCAS", "base", 1, []byte("original"), 10)

	// Begin transaction.
	h.txns.begin(60)
	hdr := rsi.RequestHeader{TxnID: 60}

	// CAS with correct expected_sequence should succeed.
	status, _ := h.handleSetValue(hdr, encodeSetValue(guid, "TxnCAS", "base", 1, []byte("updated"), 20, 10))
	if status != rsi.StatusOK {
		t.Fatalf("CAS in txn: status = %d", status)
	}

	// CAS with wrong sequence should fail.
	status, _ = h.handleSetValue(hdr, encodeSetValue(guid, "TxnCAS", "base", 1, []byte("bad"), 30, 10))
	if status != rsi.StatusCASFailed {
		t.Errorf("CAS mismatch in txn: status = %d, want CASFailed", status)
	}

	h.txns.commit(60)

	// Verify the committed value.
	var data []byte
	hive.WriteDB().QueryRow(`SELECT data FROM main.[values] WHERE key_guid = ? AND name_folded = ? AND layer = ?`,
		guid[:], "txncas", "base").Scan(&data)
	if string(data) != "updated" {
		t.Errorf("data = %q, want %q", data, "updated")
	}
}

func TestQueryValuesReadYourOwnWrites(t *testing.T) {
	h, _, guid := setupKeyForValues(t)

	// Begin transaction.
	h.txns.begin(70)
	hdr := rsi.RequestHeader{TxnID: 70}

	// Write a value within the transaction.
	status, _ := h.handleSetValue(hdr, encodeSetValue(guid, "RYOW", "base", 1, []byte("txn-data"), 100, 0))
	if status != rsi.StatusOK {
		t.Fatalf("set value in txn: status = %d", status)
	}

	// Query within the same transaction should see the uncommitted value.
	status, payload := h.handleQueryValues(hdr, encodeQueryValues(guid, "RYOW", false))
	if status != rsi.StatusOK {
		t.Fatalf("query in txn: status = %d", status)
	}

	d := rsi.NewDecoder(payload)
	count, _ := d.Uint32()
	if count != 1 {
		t.Errorf("txn read-your-own-writes: count = %d, want 1", count)
	}

	// Non-transactional query should NOT see the uncommitted value.
	status, payload = h.handleQueryValues(rsi.RequestHeader{}, encodeQueryValues(guid, "RYOW", false))
	if status != rsi.StatusOK {
		t.Fatalf("non-txn query: status = %d", status)
	}
	d = rsi.NewDecoder(payload)
	count, _ = d.Uint32()
	if count != 0 {
		t.Errorf("non-txn isolation: count = %d, want 0", count)
	}

	h.txns.abort(70)
}

func TestDeleteValueEntryVolatileKey(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0xA2}

	// Create a volatile key with a value.
	insertKey(t, hive, guid, "VolDelKey", root, true)
	h.guidCache.Store(guid, hive)

	_, err := hive.WriteDB().Exec(`
		INSERT INTO volatile.[values] (key_guid, name, name_folded, layer, type, data, sequence)
		VALUES (?, 'VolDel', ?, 'base', 1, X'FF', 1)
	`, guid[:], fold.String("VolDel"))
	if err != nil {
		t.Fatal(err)
	}

	status, _ := h.handleDeleteValueEntry(rsi.RequestHeader{}, encodeDeleteValueEntry(guid, "VolDel", "base"))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	// Value should be gone from volatile.
	var count int
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM volatile.[values] WHERE key_guid = ?`, guid[:]).Scan(&count)
	if count != 0 {
		t.Error("volatile value entry not deleted")
	}
}

func TestSetBlanketTombstoneVolatileKey(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)
	guid := rsi.GUID{0xA3}

	insertKey(t, hive, guid, "VolBTKey", root, true)
	h.guidCache.Store(guid, hive)

	status, _ := h.handleSetBlanketTombstone(rsi.RequestHeader{}, encodeBlanketTombstone(guid, "gpo-1", true, 200))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	// Blanket tombstone should be in volatile database.
	var layer string
	hive.WriteDB().QueryRow(`SELECT layer FROM volatile.blanket_tombstones WHERE key_guid = ?`, guid[:]).Scan(&layer)
	if layer != "gpo-1" {
		t.Errorf("volatile blanket tombstone layer = %q, want %q", layer, "gpo-1")
	}

	// Should NOT be in main database.
	var count int
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM main.blanket_tombstones WHERE key_guid = ?`, guid[:]).Scan(&count)
	if count != 0 {
		t.Errorf("main blanket_tombstones count = %d, want 0", count)
	}
}

func TestDeleteValueEntryUnknownGUID(t *testing.T) {
	h, _, _ := setupKeyForValues(t)
	status, _ := h.handleDeleteValueEntry(rsi.RequestHeader{}, encodeDeleteValueEntry(rsi.GUID{0xFF}, "x", "base"))
	if status != rsi.StatusNotFound {
		t.Errorf("status = %d, want NotFound for unknown GUID", status)
	}
}

// --- RSI_DELETE_VALUE_ENTRY tests ---

func TestDeleteValueEntry(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	insertValue(t, hive, guid, "DelMe", "base", 1, []byte("x"), 1)

	status, _ := h.handleDeleteValueEntry(rsi.RequestHeader{}, encodeDeleteValueEntry(guid, "DelMe", "base"))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}

	var count int
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM main.[values] WHERE key_guid = ? AND name_folded = ?`,
		guid[:], "delme").Scan(&count)
	if count != 0 {
		t.Error("value entry not deleted")
	}
}

func TestDeleteValueEntryIdempotent(t *testing.T) {
	h, _, guid := setupKeyForValues(t)
	status, _ := h.handleDeleteValueEntry(rsi.RequestHeader{}, encodeDeleteValueEntry(guid, "noexist", "base"))
	if status != rsi.StatusOK {
		t.Errorf("idempotent delete status = %d, want OK", status)
	}
}

// --- RSI_SET_BLANKET_TOMBSTONE tests ---

func TestSetBlanketTombstone(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)

	status, _ := h.handleSetBlanketTombstone(rsi.RequestHeader{}, encodeBlanketTombstone(guid, "gpo-1", true, 100))
	if status != rsi.StatusOK {
		t.Fatalf("set: status = %d", status)
	}

	var layer string
	var seq uint64
	hive.WriteDB().QueryRow(`SELECT layer, sequence FROM main.blanket_tombstones WHERE key_guid = ?`, guid[:]).Scan(&layer, &seq)
	if layer != "gpo-1" {
		t.Errorf("layer = %q", layer)
	}
	if seq != 100 {
		t.Errorf("sequence = %d", seq)
	}
}

func TestRemoveBlanketTombstone(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)

	// Set then remove.
	h.handleSetBlanketTombstone(rsi.RequestHeader{}, encodeBlanketTombstone(guid, "gpo-1", true, 100))
	status, _ := h.handleSetBlanketTombstone(rsi.RequestHeader{}, encodeBlanketTombstone(guid, "gpo-1", false, 0))
	if status != rsi.StatusOK {
		t.Fatalf("remove: status = %d", status)
	}

	var count int
	hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM main.blanket_tombstones WHERE key_guid = ?`, guid[:]).Scan(&count)
	if count != 0 {
		t.Error("blanket tombstone not removed")
	}
}

func TestSetBlanketTombstoneKeyNotFound(t *testing.T) {
	h, _, _ := setupKeyForValues(t)
	status, _ := h.handleSetBlanketTombstone(rsi.RequestHeader{}, encodeBlanketTombstone(rsi.GUID{0xFF}, "x", true, 1))
	if status != rsi.StatusNotFound {
		t.Errorf("status = %d, want NotFound", status)
	}
}

// --- Payload builders ---

func encodeQueryValues(guid rsi.GUID, valueName string, queryAll bool) []byte {
	e := rsi.NewEncoder(64)
	e.PutGUID(guid)
	e.PutString(valueName)
	e.PutUint8(boolByte(queryAll))
	return e.Bytes()
}

func encodeSetValue(guid rsi.GUID, valueName, layer string, typ uint32, data []byte, seq, expectedSeq uint64) []byte {
	e := rsi.NewEncoder(128)
	e.PutGUID(guid)
	e.PutString(valueName)
	e.PutString(layer)
	e.PutUint32(typ)
	e.PutBlob(data)
	e.PutUint64(seq)
	e.PutUint64(expectedSeq)
	return e.Bytes()
}

func encodeDeleteValueEntry(guid rsi.GUID, valueName, layer string) []byte {
	e := rsi.NewEncoder(64)
	e.PutGUID(guid)
	e.PutString(valueName)
	e.PutString(layer)
	return e.Bytes()
}

func encodeBlanketTombstone(guid rsi.GUID, layer string, set bool, seq uint64) []byte {
	e := rsi.NewEncoder(64)
	e.PutGUID(guid)
	e.PutString(layer)
	e.PutUint8(boolByte(set))
	e.PutUint64(seq)
	return e.Bytes()
}
