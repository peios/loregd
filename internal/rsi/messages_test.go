package rsi

import (
	"bytes"
	"testing"
)

// buildPayload encodes fields into a wire-format payload.
func buildPayload(fn func(e *Encoder)) []byte {
	e := NewEncoder(128)
	fn(e)
	return e.Bytes()
}

func TestLookupRequestRoundTrip(t *testing.T) {
	parent := GUID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	payload := buildPayload(func(e *Encoder) {
		e.PutGUID(parent)
		e.PutString("System")
	})

	req, err := DecodeLookupRequest(NewDecoder(payload))
	if err != nil {
		t.Fatal(err)
	}
	if req.ParentGUID != parent {
		t.Errorf("ParentGUID = %x, want %x", req.ParentGUID, parent)
	}
	if req.ChildName != "System" {
		t.Errorf("ChildName = %q, want %q", req.ChildName, "System")
	}
}

func TestLookupResponseEncoding(t *testing.T) {
	guid1 := GUID{0xAA}
	entries := []LookupPathEntry{
		{LayerName: "base", TargetType: TargetGUID, TargetGUID: guid1, Sequence: 42},
		{LayerName: "role-x", TargetType: TargetHidden, Sequence: 50},
	}
	meta := []LookupKeyMeta{
		{GUID: guid1, SD: []byte{0x01, 0x02}, Volatile: false, Symlink: false, LastWriteTime: 1000},
	}

	e := NewEncoder(128)
	EncodeLookupResponse(e, entries, meta)

	d := NewDecoder(e.Bytes())
	count, _ := d.Uint32()
	if count != 2 {
		t.Fatalf("entry count = %d, want 2", count)
	}

	// Entry 1
	layer, _ := d.String()
	if layer != "base" {
		t.Errorf("entry[0].layer = %q, want %q", layer, "base")
	}
	tt, _ := d.Uint8()
	if tt != TargetGUID {
		t.Errorf("entry[0].target_type = %d, want %d", tt, TargetGUID)
	}
	g, _ := d.GUID()
	if g != guid1 {
		t.Errorf("entry[0].guid = %x", g)
	}
	seq, _ := d.Uint64()
	if seq != 42 {
		t.Errorf("entry[0].sequence = %d, want 42", seq)
	}

	// Entry 2
	layer, _ = d.String()
	if layer != "role-x" {
		t.Errorf("entry[1].layer = %q", layer)
	}
	tt, _ = d.Uint8()
	if tt != TargetHidden {
		t.Errorf("entry[1].target_type = %d, want %d", tt, TargetHidden)
	}

	// Skip GUID and sequence for entry 2
	d.GUID()
	d.Uint64()

	// Metadata
	metaCount, _ := d.Uint32()
	if metaCount != 1 {
		t.Fatalf("meta count = %d, want 1", metaCount)
	}
	mg, _ := d.GUID()
	if mg != guid1 {
		t.Errorf("meta[0].guid = %x", mg)
	}
	sd, _ := d.Blob()
	if !bytes.Equal(sd, []byte{0x01, 0x02}) {
		t.Errorf("meta[0].sd = %x", sd)
	}
}

func TestDecoderIgnoresTrailingBytes(t *testing.T) {
	// All decoders must silently ignore unknown trailing bytes
	// to support forward compatibility (spec: "Sources MUST ignore
	// fields they do not recognise").
	tests := []struct {
		name   string
		decode func(d *Decoder) error
		build  func(e *Encoder)
	}{
		{
			"LookupRequest",
			func(d *Decoder) error { _, err := DecodeLookupRequest(d); return err },
			func(e *Encoder) {
				e.PutGUID(GUID{1})
				e.PutString("name")
			},
		},
		{
			"CreateEntryRequest",
			func(d *Decoder) error { _, err := DecodeCreateEntryRequest(d); return err },
			func(e *Encoder) {
				e.PutGUID(GUID{1})
				e.PutString("child")
				e.PutString("layer")
				e.PutGUID(GUID{2})
				e.PutUint64(1)
			},
		},
		{
			"CreateKeyRequest",
			func(d *Decoder) error { _, err := DecodeCreateKeyRequest(d); return err },
			func(e *Encoder) {
				e.PutGUID(GUID{1})
				e.PutString("key")
				e.PutGUID(GUID{2})
				e.PutBlob([]byte{0x01})
				e.PutUint8(0)
				e.PutUint8(0)
			},
		},
		{
			"SetValueRequest",
			func(d *Decoder) error { _, err := DecodeSetValueRequest(d); return err },
			func(e *Encoder) {
				e.PutGUID(GUID{1})
				e.PutString("val")
				e.PutString("layer")
				e.PutUint32(1)
				e.PutBlob([]byte{0x00})
				e.PutUint64(1)
				e.PutUint64(0)
			},
		},
		{
			"BeginTransactionRequest",
			func(d *Decoder) error { _, err := DecodeBeginTransactionRequest(d); return err },
			func(e *Encoder) { e.PutUint64(42) },
		},
		{
			"DeleteLayerRequest",
			func(d *Decoder) error { _, err := DecodeDeleteLayerRequest(d); return err },
			func(e *Encoder) { e.PutString("layer-x") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewEncoder(128)
			tt.build(e)
			// Append unknown future fields.
			e.PutUint32(0xDEADBEEF)
			e.PutString("future_field")
			e.PutBytes([]byte{0xFF, 0xFE, 0xFD})

			d := NewDecoder(e.Bytes())
			if err := tt.decode(d); err != nil {
				t.Fatalf("decode failed with trailing bytes: %v", err)
			}
			if d.Remaining() == 0 {
				t.Fatal("expected trailing bytes to remain, got 0")
			}
		})
	}
}

func TestLookupResponseHiddenEntryGUIDZeroed(t *testing.T) {
	// Per RSI wire spec: HIDDEN entries have target_guid zeroed.
	entries := []LookupPathEntry{
		{LayerName: "role-x", TargetType: TargetHidden, Sequence: 50},
	}

	e := NewEncoder(64)
	EncodeLookupResponse(e, entries, nil)

	d := NewDecoder(e.Bytes())
	count, _ := d.Uint32()
	if count != 1 {
		t.Fatalf("count = %d", count)
	}
	d.String() // layer
	d.Uint8()  // target_type

	guid, _ := d.GUID()
	if guid != (GUID{}) {
		t.Errorf("HIDDEN entry GUID = %x, want zeroed", guid)
	}
}

func TestCreateEntryRequestRoundTrip(t *testing.T) {
	parent := GUID{0x01}
	child := GUID{0x02}
	payload := buildPayload(func(e *Encoder) {
		e.PutGUID(parent)
		e.PutString("child")
		e.PutString("base")
		e.PutGUID(child)
		e.PutUint64(99)
	})

	req, err := DecodeCreateEntryRequest(NewDecoder(payload))
	if err != nil {
		t.Fatal(err)
	}
	if req.ParentGUID != parent {
		t.Error("parent mismatch")
	}
	if req.ChildName != "child" {
		t.Errorf("ChildName = %q", req.ChildName)
	}
	if req.LayerName != "base" {
		t.Errorf("LayerName = %q", req.LayerName)
	}
	if req.ChildGUID != child {
		t.Error("child guid mismatch")
	}
	if req.Sequence != 99 {
		t.Errorf("Sequence = %d", req.Sequence)
	}
}

func TestCreateKeyRequestRoundTrip(t *testing.T) {
	guid := GUID{0xAA}
	parent := GUID{0xBB}
	sd := []byte{0x01, 0x00, 0x04, 0x80} // minimal SD header
	payload := buildPayload(func(e *Encoder) {
		e.PutGUID(guid)
		e.PutString("MyKey")
		e.PutGUID(parent)
		e.PutBlob(sd)
		e.PutUint8(0) // volatile
		e.PutUint8(1) // symlink
	})

	req, err := DecodeCreateKeyRequest(NewDecoder(payload))
	if err != nil {
		t.Fatal(err)
	}
	if req.GUID != guid {
		t.Error("GUID mismatch")
	}
	if req.Name != "MyKey" {
		t.Errorf("Name = %q", req.Name)
	}
	if req.ParentGUID != parent {
		t.Error("parent mismatch")
	}
	if !bytes.Equal(req.SD, sd) {
		t.Error("SD mismatch")
	}
	if req.Volatile {
		t.Error("expected non-volatile")
	}
	if !req.Symlink {
		t.Error("expected symlink")
	}
}

func TestSetValueRequestRoundTrip(t *testing.T) {
	guid := GUID{0x01}
	payload := buildPayload(func(e *Encoder) {
		e.PutGUID(guid)
		e.PutString("Precedence")
		e.PutString("base")
		e.PutUint32(4) // REG_DWORD
		e.PutBlob([]byte{0x00, 0x00, 0x00, 0x00})
		e.PutUint64(10) // sequence
		e.PutUint64(5)  // expected_sequence
	})

	req, err := DecodeSetValueRequest(NewDecoder(payload))
	if err != nil {
		t.Fatal(err)
	}
	if req.GUID != guid {
		t.Error("GUID mismatch")
	}
	if req.ValueName != "Precedence" {
		t.Errorf("ValueName = %q", req.ValueName)
	}
	if req.LayerName != "base" {
		t.Errorf("LayerName = %q", req.LayerName)
	}
	if req.Type != 4 {
		t.Errorf("Type = %d", req.Type)
	}
	if req.Sequence != 10 {
		t.Errorf("Sequence = %d", req.Sequence)
	}
	if req.ExpectedSequence != 5 {
		t.Errorf("ExpectedSequence = %d", req.ExpectedSequence)
	}
}

func TestWriteKeyRequestConditionalFields(t *testing.T) {
	guid := GUID{0x01}

	// Only SD
	payload := buildPayload(func(e *Encoder) {
		e.PutGUID(guid)
		e.PutUint32(WriteKeyFieldSD)
		e.PutBlob([]byte{0xAA})
	})
	req, err := DecodeWriteKeyRequest(NewDecoder(payload))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(req.SD, []byte{0xAA}) {
		t.Error("SD mismatch")
	}
	if req.LastWriteTime != 0 {
		t.Error("last_write_time should be zero")
	}

	// Only last_write_time
	payload = buildPayload(func(e *Encoder) {
		e.PutGUID(guid)
		e.PutUint32(WriteKeyFieldLastWriteTime)
		e.PutUint64(12345)
	})
	req, err = DecodeWriteKeyRequest(NewDecoder(payload))
	if err != nil {
		t.Fatal(err)
	}
	if req.SD != nil {
		t.Error("SD should be nil")
	}
	if req.LastWriteTime != 12345 {
		t.Errorf("LastWriteTime = %d", req.LastWriteTime)
	}

	// Both fields
	payload = buildPayload(func(e *Encoder) {
		e.PutGUID(guid)
		e.PutUint32(WriteKeyFieldSD | WriteKeyFieldLastWriteTime)
		e.PutBlob([]byte{0xBB})
		e.PutUint64(99999)
	})
	req, err = DecodeWriteKeyRequest(NewDecoder(payload))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(req.SD, []byte{0xBB}) {
		t.Error("SD mismatch")
	}
	if req.LastWriteTime != 99999 {
		t.Errorf("LastWriteTime = %d", req.LastWriteTime)
	}
}

func TestDeleteLayerResponseEncoding(t *testing.T) {
	orphans := []GUID{{0x01}, {0x02}, {0x03}}
	e := NewEncoder(64)
	EncodeDeleteLayerResponse(e, orphans)

	d := NewDecoder(e.Bytes())
	count, _ := d.Uint32()
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
	for i, expected := range orphans {
		g, _ := d.GUID()
		if g != expected {
			t.Errorf("orphan[%d] = %x, want %x", i, g, expected)
		}
	}
}

func TestQueryValuesResponseEncoding(t *testing.T) {
	entries := []QueryValuesEntry{
		{ValueName: "Name", LayerName: "base", Type: 1, Data: []byte("hello"), Sequence: 10},
	}
	blankets := []BlanketTombstoneEntry{
		{LayerName: "gpo-1", Sequence: 20},
	}

	e := NewEncoder(128)
	EncodeQueryValuesResponse(e, entries, blankets)

	d := NewDecoder(e.Bytes())
	entryCount, _ := d.Uint32()
	if entryCount != 1 {
		t.Fatalf("entry count = %d", entryCount)
	}
	vn, _ := d.String()
	if vn != "Name" {
		t.Errorf("value name = %q", vn)
	}
	ln, _ := d.String()
	if ln != "base" {
		t.Errorf("layer name = %q", ln)
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

	blanketCount, _ := d.Uint32()
	if blanketCount != 1 {
		t.Fatalf("blanket count = %d", blanketCount)
	}
	bln, _ := d.String()
	if bln != "gpo-1" {
		t.Errorf("blanket layer = %q", bln)
	}
	bseq, _ := d.Uint64()
	if bseq != 20 {
		t.Errorf("blanket sequence = %d", bseq)
	}
}
