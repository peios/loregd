package rsi

import (
	"fmt"
	"testing"
)

// TestDecodeBeginTransactionMode covers the RSI_BEGIN_TRANSACTION mode field
// (PSPK §4.4): both defined modes decode only with their complete payload.
func TestDecodeBeginTransactionMode(t *testing.T) {
	cases := []struct {
		name     string
		build    func(*Encoder)
		wantTxn  uint64
		wantMode uint32
	}{
		{
			name:     "read-write explicit",
			build:    func(e *Encoder) { e.PutUint64(7); e.PutUint32(TxnReadWrite) },
			wantTxn:  7,
			wantMode: TxnReadWrite,
		},
		{
			name:     "read-only explicit",
			build:    func(e *Encoder) { e.PutUint64(8); e.PutUint32(TxnReadOnly) },
			wantTxn:  8,
			wantMode: TxnReadOnly,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEncoder(12)
			tc.build(e)
			r, err := DecodeBeginTransactionRequest(NewDecoder(e.Bytes()))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if r.TxnID != tc.wantTxn {
				t.Errorf("TxnID = %d, want %d", r.TxnID, tc.wantTxn)
			}
			if r.Mode != tc.wantMode {
				t.Errorf("Mode = %d, want %d", r.Mode, tc.wantMode)
			}
		})
	}
}

func TestDecodeBeginTransactionRejectsTruncation(t *testing.T) {
	for _, mode := range []uint32{TxnReadWrite, TxnReadOnly} {
		e := NewEncoder(12)
		e.PutUint64(9)
		e.PutUint32(mode)
		payload := e.Bytes()
		for n := 0; n < len(payload); n++ {
			t.Run(fmt.Sprintf("mode-%d/bytes-%d", mode, n), func(t *testing.T) {
				if _, err := DecodeBeginTransactionRequest(NewDecoder(payload[:n])); err == nil {
					t.Fatal("truncated BEGIN decoded without error")
				}
			})
		}
	}
}
