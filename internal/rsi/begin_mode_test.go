package rsi

import "testing"

// TestDecodeBeginTransactionMode covers the RSI_BEGIN_TRANSACTION mode field
// (PSD-005 §11.3): present modes decode, and an absent mode defaults to
// read-write (tolerant decode for the source-agnostic begin path).
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
		{
			name:     "mode absent defaults read-write",
			build:    func(e *Encoder) { e.PutUint64(9) },
			wantTxn:  9,
			wantMode: TxnReadWrite,
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
