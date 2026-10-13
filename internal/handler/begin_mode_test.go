package handler

import (
	"fmt"
	"testing"

	"github.com/peios/loregd/internal/rsi"
)

// A malformed BEGIN must neither reserve its ID nor permit a later write.
// Exercise the handler, not just the decoder: the old handler mapped every
// complete mode other than READ_ONLY to a writable transaction.
func TestBeginTransactionRejectsMalformedModeWithoutState(t *testing.T) {
	const txnID = 901
	valid := encodeBeginTxnMode(txnID, rsi.TxnReadOnly)
	type malformedCase struct {
		name    string
		payload []byte
	}
	var cases []malformedCase
	for n := 0; n < len(valid); n++ {
		cases = append(cases, malformedCase{
			fmt.Sprintf("truncated-%d", n), valid[:n],
		})
	}
	for _, mode := range []uint32{2, 0xffffffff} {
		cases = append(cases, malformedCase{
			fmt.Sprintf("unknown-mode-%d", mode), encodeBeginTxnMode(txnID, mode),
		})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, hive, guid := setupKeyForValues(t)
			status, _ := h.handleBeginTransaction(rsi.RequestHeader{}, tc.payload)
			if status != rsi.StatusInvalid {
				t.Errorf("malformed BEGIN status = %d, want INVALID", status)
			}
			h.txns.mu.Lock()
			pending := len(h.txns.txns)
			h.txns.mu.Unlock()
			if pending != 0 {
				t.Fatalf("malformed BEGIN recorded %d transactions", pending)
			}

			status, _ = h.handleSetValue(rsi.RequestHeader{TxnID: txnID},
				encodeSetValue(guid, "MustNotWrite", "base", 1, []byte("x"), 1, 0))
			if status != rsi.StatusStorageError {
				t.Errorf("write with rejected ID = %d, want STORAGE_ERROR", status)
			}
			var count int
			if err := hive.WriteDB().QueryRow(`SELECT COUNT(*) FROM main.[values] WHERE key_guid = ? AND name_folded = ?`,
				guid[:], "mustnotwrite").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Error("write through rejected transaction changed the database")
			}

			// The ID remains usable for a subsequent well-formed request.
			status, _ = h.handleBeginTransaction(rsi.RequestHeader{}, valid)
			if status != rsi.StatusOK {
				t.Fatalf("valid BEGIN after rejection = %d, want OK", status)
			}
			h.txns.mu.Lock()
			info := h.txns.txns[txnID]
			readOnly := info != nil && info.readOnly
			h.txns.mu.Unlock()
			if !readOnly {
				t.Error("valid read-only BEGIN lost its mode")
			}
			h.handleAbortTransaction(rsi.RequestHeader{}, encodeAbortTxn(txnID))
		})
	}
}
