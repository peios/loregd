package handler

import (
	"bytes"

	"github.com/peios/loregd/internal/hivedb"
	"reflect"
	"testing"

	"github.com/peios/loregd/internal/rsi"
)

// The RSI's deterministic-enumeration obligation covers the blanket
// tombstone list of RSI_QUERY_VALUES as well as the value entries beside
// it. The list came straight from an unordered UNION ALL across the
// persistent and volatile stores, which SQLite does not guarantee to be
// stable — so it was the one enumeration block left uncanonicalised.
func TestBlanketTombstonesAreReturnedInACanonicalOrder(t *testing.T) {
	h, hive, guid := setupKeyForValues(t)
	insertValue(t, hive, guid, "V", "base", 1, []byte("v"), 1)

	// Deliberately inserted out of order, and split across both stores so
	// the UNION ALL has two arms to interleave.
	persistent := []struct {
		layer string
		seq   uint64
	}{{"zeta", 30}, {"alpha", 10}, {"MIDDLE", 20}}
	for _, b := range persistent {
		if _, err := hive.WriteDB().Exec(
			`INSERT INTO main.blanket_tombstones (key_guid, layer, sequence) VALUES (?, ?, ?)`,
			guid[:], b.layer, b.seq); err != nil {
			t.Fatalf("insert persistent blanket %s: %v", b.layer, err)
		}
	}
	volatileRows := []struct {
		layer string
		seq   uint64
	}{{"beta", 15}, {"alpha", 5}}
	for _, b := range volatileRows {
		if _, err := hive.WriteDB().Exec(
			`INSERT INTO volatile.blanket_tombstones (key_guid, layer, sequence) VALUES (?, ?, ?)`,
			guid[:], b.layer, b.seq); err != nil {
			t.Fatalf("insert volatile blanket %s: %v", b.layer, err)
		}
	}

	// Folded layer name, then sequence — the same key the value entries
	// beside them use.
	want := []string{"alpha/5", "alpha/10", "beta/15", "MIDDLE/20", "zeta/30"}

	var first []string
	for call := range 50 {
		status, payload := h.handleQueryValues(rsi.RequestHeader{}, encodeQueryValues(guid, "", true))
		if status != rsi.StatusOK {
			t.Fatalf("call %d: status = %d", call, status)
		}
		got := decodeBlanketList(t, payload)
		if call == 0 {
			first = got
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("blanket order = %v, want %v", got, want)
			}
			continue
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("call %d order = %v, differs from the first call %v", call, got, first)
		}
	}
}

// decodeBlanketList reads a QUERY_VALUES response and returns its blanket
// tombstones as "layer/sequence" strings, in wire order.
func decodeBlanketList(t *testing.T, payload []byte) []string {
	t.Helper()
	d := rsi.NewDecoder(payload)
	entryCount, _ := d.Uint32()
	for range entryCount {
		d.String() // name
		d.String() // layer
		d.Uint32() // type
		d.Blob()   // data
		d.Uint64() // sequence
	}
	blanketCount, _ := d.Uint32()
	out := make([]string, 0, blanketCount)
	for range blanketCount {
		layer, _ := d.String()
		seq, _ := d.Uint64()
		out = append(out, layer+"/"+itoa(seq))
	}
	return out
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// RSI_ENUM_CHILDREN groups children by folded name and reports one
// display name per group. When two stores hold the same name in
// different case — "Foo" and "FOO" — the group order was stable but the
// *case reported* depended on whichever row the unordered union yielded
// first, so a caller comparing successive enumerations saw the name
// change under it.
func TestEnumChildrenReportsAStableNameCase(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)

	upper := rsi.GUID{0x11}
	lower := rsi.GUID{0x22}
	insertKey(t, hive, upper, "FOO", root, false)
	insertKey(t, hive, lower, "Foo", root, false)
	insertPathEntry(t, hive, root, "FOO", "base", upper, 1)
	insertPathEntry(t, hive, root, "Foo", "vendor", lower, 2)

	// One child, and the lowest bytewise spelling of it: a property of
	// the set rather than of row order. "FOO" < "Foo" in ASCII.
	want := []string{"FOO"}

	for call := range 50 {
		status, payload := h.handleEnumChildren(rsi.RequestHeader{}, encodeEnumChildren(root))
		if status != rsi.StatusOK {
			t.Fatalf("call %d: status = %d", call, status)
		}
		if got := decodeEnumChildNames(t, payload); !reflect.DeepEqual(got, want) {
			t.Fatalf("call %d: child names = %v, want %v", call, got, want)
		}
	}
}

// The RSI_DELETE_LAYER orphan array was concatenated across a range over
// h.hives, which is a Go map — so identical calls returned the same GUIDs
// in different orders.
func TestDeleteLayerOrphansAreReturnedInByteOrder(t *testing.T) {
	hiveA := openTestHive(t, "Machine")
	hiveB := openTestHive(t, "Users")
	hiveC := openTestHive(t, "Policy")
	h := New([]*hivedb.HiveDB{hiveA, hiveB, hiveC})

	// One orphan per hive, deliberately assigned so that hive order and
	// byte order disagree.
	seed := []struct {
		hive *hivedb.HiveDB
		guid rsi.GUID
	}{
		{hiveA, rsi.GUID{0xCC}},
		{hiveB, rsi.GUID{0x11}},
		{hiveC, rsi.GUID{0x77}},
	}
	for _, s := range seed {
		root := rsi.GUID(s.hive.RootGUID)
		insertKey(t, s.hive, s.guid, "Doomed", root, false)
		insertPathEntry(t, s.hive, root, "Doomed", "condemned", s.guid, 1)
	}

	want := []rsi.GUID{{0x11}, {0x77}, {0xCC}}

	status, payload := h.handleDeleteLayer(rsi.RequestHeader{}, encodeDeleteLayer("condemned"))
	if status != rsi.StatusOK {
		t.Fatalf("status = %d", status)
	}
	got := decodeOrphanGUIDs(t, payload)
	if len(got) != len(want) {
		t.Fatalf("orphan count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i][:], want[i][:]) {
			t.Fatalf("orphan %d = %x, want %x (array is not in byte order)", i, got[i], want[i])
		}
	}
}

func decodeOrphanGUIDs(t *testing.T, payload []byte) []rsi.GUID {
	t.Helper()
	d := rsi.NewDecoder(payload)
	n, _ := d.Uint32()
	out := make([]rsi.GUID, 0, n)
	for range n {
		g, err := d.GUID()
		if err != nil {
			t.Fatalf("truncated orphan array: %v", err)
		}
		out = append(out, g)
	}
	return out
}

// resolveHive probes each hive in turn and previously cached-and-returned
// only when err == nil, falling through to the next hive on *any* other
// error. When every hive had been tried it returned nil, and every caller
// maps nil to RSI_NOT_FOUND.
//
// So a database that has become unreadable was reported to LCS as "this
// key does not exist" — the worst available mapping. The kernel cannot
// distinguish a key that was deleted from one it can no longer read, and
// a caller sees a clean negative answer rather than something to retry or
// escalate.
func TestAnUnreadableHiveReportsAStorageErrorNotNotFound(t *testing.T) {
	h, hive := testHandler(t)
	root := rsi.GUID(hive.RootGUID)

	child := rsi.GUID{0x42}
	insertKey(t, hive, child, "Child", root, false)
	insertPathEntry(t, hive, root, "Child", "base", child, 1)

	// Sanity: the key resolves while the hive is readable.
	if status, _ := h.handleReadKey(rsi.RequestHeader{}, encodeReadKey(child)); status != rsi.StatusOK {
		t.Fatalf("precondition: read key status = %d, want OK", status)
	}

	// Take the hive away underneath the handler, and drop the positive
	// cache entry the successful read just installed — otherwise the
	// cache answers before any probe runs.
	h.guidCache.Delete(child)
	if err := hive.Close(); err != nil {
		t.Fatalf("close hive: %v", err)
	}

	// Every operation that resolves a GUID must now report a storage
	// failure rather than a negative answer.
	for name, call := range map[string]func() uint32{
		"read key": func() uint32 {
			st, _ := h.handleReadKey(rsi.RequestHeader{}, encodeReadKey(child))
			return st
		},
		"lookup": func() uint32 {
			st, _ := h.handleLookup(rsi.RequestHeader{}, encodeLookup(child, "anything"))
			return st
		},
		"enum children": func() uint32 {
			st, _ := h.handleEnumChildren(rsi.RequestHeader{}, encodeEnumChildren(child))
			return st
		},
		"query values": func() uint32 {
			st, _ := h.handleQueryValues(rsi.RequestHeader{}, encodeQueryValues(child, "v", false))
			return st
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := call()
			if got == rsi.StatusNotFound {
				t.Fatalf("status = RSI_NOT_FOUND; an unreadable hive must not read as an absent key")
			}
			if got != rsi.StatusStorageError {
				t.Errorf("status = %d, want RSI_STORAGE_ERROR (%d)", got, rsi.StatusStorageError)
			}
		})
	}
}

// The converse must keep working: a GUID no hive holds is genuinely
// absent, and must still answer RSI_NOT_FOUND rather than a storage
// error.
func TestAnAbsentKeyStillReportsNotFound(t *testing.T) {
	h, _ := testHandler(t)
	absent := rsi.GUID{0xDE, 0xAD}

	if st, _ := h.handleReadKey(rsi.RequestHeader{}, encodeReadKey(absent)); st != rsi.StatusNotFound {
		t.Errorf("read key: status = %d, want RSI_NOT_FOUND", st)
	}
	if st, _ := h.handleQueryValues(rsi.RequestHeader{}, encodeQueryValues(absent, "v", false)); st != rsi.StatusNotFound {
		t.Errorf("query values: status = %d, want RSI_NOT_FOUND", st)
	}
}
