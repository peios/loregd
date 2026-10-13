package handler

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/peios/loregd/internal/rsi"
)

// External database edits can contradict the redundant volatile column. The
// containing schema still determines where an existing key's writes belong.
func TestExistingKeyWritesUseContainingSchema(t *testing.T) {
	for _, schema := range []string{"main", "volatile"} {
		t.Run(schema, func(t *testing.T) {
			h, hive := testHandler(t)
			root := rsi.GUID(hive.RootGUID)
			guid := rsi.GUID{0xD7}
			other, wrongFlag := "volatile", 1
			if schema == "volatile" {
				other, wrongFlag = "main", 0
			}
			insertKey(t, hive, guid, "Contradictory", root, schema == "volatile")
			db := hive.WriteDB()
			if _, err := db.Exec("UPDATE "+schema+".keys SET volatile = ? WHERE guid = ?", wrongFlag, guid[:]); err != nil {
				t.Fatal(err)
			}
			// Deliberately retain rows in the other store for the same GUID.
			// A misrouted upsert must not silently overwrite these sentinels.
			if _, err := db.Exec("INSERT INTO "+other+".[values] (key_guid, name, name_folded, layer, type, data, sequence) VALUES (?, 'Value', 'value', 'base', 1, ?, 7)", guid[:], []byte("other")); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO "+other+".blanket_tombstones (key_guid, layer, sequence) VALUES (?, 'base', 7)", guid[:]); err != nil {
				t.Fatal(err)
			}
			for _, op := range []struct {
				name string
				call func() (uint32, []byte)
			}{
				{"write key", func() (uint32, []byte) {
					return h.handleWriteKey(rsi.RequestHeader{}, encodeWriteKey(guid, rsi.WriteKeyFieldSD|rsi.WriteKeyFieldLastWriteTime, []byte("new-sd"), 42))
				}},
				{"set value", func() (uint32, []byte) {
					return h.handleSetValue(rsi.RequestHeader{}, encodeSetValue(guid, "Value", "base", 1, []byte("new-value"), 42, 0))
				}},
				{"blanket tombstone", func() (uint32, []byte) {
					return h.handleSetBlanketTombstone(rsi.RequestHeader{}, encodeBlanketTombstone(guid, "base", true, 42))
				}},
				{"create entry", func() (uint32, []byte) {
					return h.handleCreateEntry(rsi.RequestHeader{}, encodeCreateEntry(root, "Contradictory", "base", guid, 42))
				}},
				{"hide entry", func() (uint32, []byte) {
					return h.handleHideEntry(rsi.RequestHeader{}, encodeHideEntry(guid, "Hidden", "base", 42))
				}},
			} {
				if status, _ := op.call(); status != rsi.StatusOK {
					t.Errorf("%s: status = %d, want OK", op.name, status)
				}
			}
			var sd []byte
			var when int64
			var flag int
			if err := db.QueryRow("SELECT sd, last_write_time, volatile FROM "+schema+".keys WHERE guid = ?", guid[:]).Scan(&sd, &when, &flag); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(sd, []byte("new-sd")) || when != 42 || flag != wrongFlag {
				t.Errorf("actual key row = (%q, %d, %d), want (new-sd, 42, %d)", sd, when, flag, wrongFlag)
			}
			for _, store := range []string{other, schema} {
				wantData, wantSeq, wantEntries := "new-value", uint64(42), 2
				if store == other {
					wantData, wantSeq, wantEntries = "other", 7, 0
				}
				var data []byte
				var seq, blanketSeq uint64
				if err := db.QueryRow("SELECT data, sequence FROM "+store+".[values] WHERE key_guid = ? AND name_folded = 'value' AND layer = 'base'", guid[:]).Scan(&data, &seq); err != nil {
					t.Fatal(err)
				}
				if string(data) != wantData || seq != wantSeq {
					t.Errorf("%s value = (%q, %d), want (%q, %d)", store, data, seq, wantData, wantSeq)
				}
				if err := db.QueryRow("SELECT sequence FROM "+store+".blanket_tombstones WHERE key_guid = ? AND layer = 'base'", guid[:]).Scan(&blanketSeq); err != nil {
					t.Fatal(err)
				}
				if blanketSeq != wantSeq {
					t.Errorf("%s blanket sequence = %d, want %d", store, blanketSeq, wantSeq)
				}
				var entries int
				if err := db.QueryRow("SELECT COUNT(*) FROM "+store+".path_entries WHERE target_guid = ? OR parent_guid = ?", guid[:], guid[:]).Scan(&entries); err != nil {
					t.Fatal(err)
				}
				if entries != wantEntries {
					t.Errorf("%s path entries = %d, want %d", store, entries, wantEntries)
				}
			}
			// Reads still report stored metadata; routing neither repairs it
			// nor changes the existing read-key/lookup response contract.
			full, err := readKeyFull(db, guid)
			if err != nil {
				t.Fatal(err)
			}
			meta, err := readKeyMeta(db, guid)
			if err != nil {
				t.Fatal(err)
			}
			if full.Volatile != (wrongFlag != 0) || meta.Volatile != (wrongFlag != 0) {
				t.Fatal("routing changed the stored volatile metadata reported by reads")
			}
		})
	}
}

func TestExistingKeyRoutingKeepsMainFirstPrecedence(t *testing.T) {
	h, hive := testHandler(t)
	root, guid := rsi.GUID(hive.RootGUID), rsi.GUID{0xD8}
	// The public create path rejects duplicate GUIDs. This deliberately
	// corrupt fixture verifies the helper's existing main-first precedence.
	insertKey(t, hive, guid, "Main", root, false)
	insertKey(t, hive, guid, "Volatile", root, true)
	if _, err := hive.WriteDB().Exec("UPDATE main.keys SET volatile = 1 WHERE guid = ?", guid[:]); err != nil {
		t.Fatal(err)
	}
	status, _ := h.handleWriteKey(rsi.RequestHeader{}, encodeWriteKey(guid, rsi.WriteKeyFieldLastWriteTime, nil, 42))
	if status != rsi.StatusOK {
		t.Fatalf("write key status = %d, want OK", status)
	}
	for _, schema := range []string{"main", "volatile"} {
		var when int64
		if err := hive.WriteDB().QueryRow("SELECT last_write_time FROM "+schema+".keys WHERE guid = ?", guid[:]).Scan(&when); err != nil {
			t.Fatal(err)
		}
		want := int64(0)
		if schema == "main" {
			want = 42
		}
		if when != want {
			t.Errorf("%s timestamp = %d, want %d", schema, when, want)
		}
	}
}

func TestExistingKeyRoutingPreservesMissingAndStorageErrors(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		_, hive := testHandler(t)
		if _, err := isVolatileQ(hive.WriteDB(), rsi.GUID{0xD9}); !errors.Is(err, errKeyNotFound) {
			t.Fatalf("missing key error = %v, want errKeyNotFound", err)
		}
	})
	for _, broken := range []string{"main", "volatile"} {
		t.Run(broken, func(t *testing.T) {
			_, hive := testHandler(t)
			guid := rsi.GUID{0xD9}
			// If main is broken, a valid volatile row must not hide its error.
			if broken == "main" {
				insertKey(t, hive, guid, "ReachableOnlyAfterError", rsi.GUID(hive.RootGUID), true)
			}
			if _, err := hive.WriteDB().Exec("DROP TABLE " + broken + ".keys"); err != nil {
				t.Fatal(err)
			}
			_, err := isVolatileQ(hive.WriteDB(), guid)
			if err == nil || errors.Is(err, errKeyNotFound) || !strings.HasPrefix(err.Error(), "isVolatileQ "+broken+":") || storageStatus(err) != rsi.StatusStorageError {
				t.Fatalf("broken %s error = %v, want wrapped storage error", broken, err)
			}
		})
	}
}
