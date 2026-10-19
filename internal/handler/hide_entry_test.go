package handler

import (
	"fmt"
	"testing"

	"github.com/peios/loregd/internal/hivedb"
	"github.com/peios/loregd/internal/rsi"
)

// The child and parent intentionally live in opposite stores: creation follows
// the child, while a hidden marker follows the parent.
func crossStoreEntry(t *testing.T, parentVolatile bool) (*Handler, *hivedb.HiveDB, rsi.GUID) {
	t.Helper()
	h, hive := testHandler(t)
	parent := rsi.GUID(hive.RootGUID)
	if parentVolatile {
		parent = rsi.GUID{0xe1}
		insertKey(t, hive, parent, "Parent", rsi.GUID(hive.RootGUID), true)
	}
	child := rsi.GUID{0xe2}
	insertKey(t, hive, child, "Child", parent, !parentVolatile)
	status, _ := h.handleCreateEntry(rsi.RequestHeader{}, encodeCreateEntry(parent, "Child", "base", child, 1))
	if status != rsi.StatusOK {
		t.Fatalf("create entry status = %d, want OK", status)
	}
	return h, hive, parent
}

func requireEntryTarget(t *testing.T, h *Handler, header rsi.RequestHeader, parent rsi.GUID, target uint8) {
	t.Helper()
	status, payload := h.handleLookup(header, encodeLookup(parent, "child"))
	if status != rsi.StatusOK {
		t.Fatalf("lookup status = %d, want OK", status)
	}
	decoder := rsi.NewDecoder(payload)
	count, err := decoder.Uint32()
	if err != nil || count != 1 {
		t.Fatalf("lookup entry count = %d, error = %v; want one entry", count, err)
	}
	layer, err := decoder.String()
	if err != nil || layer != "base" {
		t.Fatalf("lookup layer = %q, error = %v; want base", layer, err)
	}
	got, err := decoder.Uint8()
	if err != nil || got != target {
		t.Fatalf("lookup target = %d, error = %v; want %d", got, err, target)
	}
}

func TestHideEntryReplacesOppositeStore(t *testing.T) {
	for _, parentVolatile := range []bool{false, true} {
		t.Run(fmt.Sprintf("volatile_parent=%t", parentVolatile), func(t *testing.T) {
			h, hive, parent := crossStoreEntry(t, parentVolatile)
			status, _ := h.handleHideEntry(rsi.RequestHeader{}, encodeHideEntry(parent, "CHILD", "base", 2))
			if status != rsi.StatusOK {
				t.Fatalf("hide entry status = %d, want OK", status)
			}
			requireEntryTarget(t, h, rsi.RequestHeader{}, parent, rsi.TargetHidden)
			schema := "main"
			if parentVolatile {
				schema = "volatile"
			}
			var count int
			err := hive.ReadDB().QueryRow("SELECT COUNT(*) FROM "+schema+".path_entries WHERE parent_guid = ? AND child_name_folded = 'child' AND layer = 'base' AND target_type = 1", parent[:]).Scan(&count)
			if err != nil || count != 1 {
				t.Fatalf("%s hidden markers = %d, error = %v; want one", schema, count, err)
			}
		})
	}
}

func TestHideEntryReplacementFollowsTransactionOutcome(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(fmt.Sprintf("commit=%t", commit), func(t *testing.T) {
			h, _, parent := crossStoreEntry(t, false)
			if status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, encodeBeginTxn(71)); status != rsi.StatusOK {
				t.Fatalf("begin status = %d, want OK", status)
			}
			header := rsi.RequestHeader{TxnID: 71}
			if status, _ := h.handleHideEntry(header, encodeHideEntry(parent, "Child", "base", 2)); status != rsi.StatusOK {
				t.Fatalf("hide status = %d, want OK", status)
			}
			requireEntryTarget(t, h, header, parent, rsi.TargetHidden)
			requireEntryTarget(t, h, rsi.RequestHeader{}, parent, rsi.TargetGUID)
			var status uint32
			target := rsi.TargetGUID
			if commit {
				status, _ = h.handleCommitTransaction(rsi.RequestHeader{}, encodeCommitTxn(71))
				target = rsi.TargetHidden
			} else {
				status, _ = h.handleAbortTransaction(rsi.RequestHeader{}, encodeAbortTxn(71))
			}
			if status != rsi.StatusOK {
				t.Fatalf("transaction end status = %d, want OK", status)
			}
			requireEntryTarget(t, h, rsi.RequestHeader{}, parent, target)
		})
	}
}

func TestFailedHideEntryRestoresOppositeStoreAndEarlierWrites(t *testing.T) {
	for _, transactional := range []bool{false, true} {
		t.Run(fmt.Sprintf("transactional=%t", transactional), func(t *testing.T) {
			h, hive, parent := crossStoreEntry(t, false)
			_, err := hive.WriteDB().Exec(`
                CREATE TRIGGER reject_hidden_entry BEFORE INSERT ON main.path_entries
                WHEN NEW.target_type = 1
                BEGIN SELECT RAISE(ABORT, 'injected hidden-entry failure'); END
            `)
			if err != nil {
				t.Fatal(err)
			}
			header := rsi.RequestHeader{}
			earlier := rsi.GUID{0xe3}
			if transactional {
				if status, _ := h.handleBeginTransaction(header, encodeBeginTxn(72)); status != rsi.StatusOK {
					t.Fatalf("begin status = %d, want OK", status)
				}
				header.TxnID = 72
				status, _ := h.handleCreateKey(header, encodeCreateKey(earlier, "Earlier", parent, []byte{1}, false, false))
				if status != rsi.StatusOK {
					t.Fatalf("earlier create status = %d, want OK", status)
				}
			}
			status, _ := h.handleHideEntry(header, encodeHideEntry(parent, "Child", "base", 2))
			if status != rsi.StatusStorageError {
				t.Fatalf("hide status = %d, want StorageError", status)
			}
			requireEntryTarget(t, h, header, parent, rsi.TargetGUID)
			if transactional {
				if status, _ := h.handleCommitTransaction(rsi.RequestHeader{}, encodeCommitTxn(72)); status != rsi.StatusOK {
					t.Fatalf("commit status = %d, want OK", status)
				}
				if status, _ := h.handleReadKey(rsi.RequestHeader{}, encodeReadKey(earlier)); status != rsi.StatusOK {
					t.Fatalf("earlier write status = %d, want OK", status)
				}
			}
			requireEntryTarget(t, h, rsi.RequestHeader{}, parent, rsi.TargetGUID)
		})
	}
}
