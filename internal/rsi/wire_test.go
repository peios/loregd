package rsi

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestEncoderDecoder(t *testing.T) {
	e := NewEncoder(64)
	e.PutUint8(0x42)
	e.PutUint16(0x1234)
	e.PutUint32(0xDEADBEEF)
	e.PutUint64(0x0102030405060708)
	e.PutGUID(GUID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	e.PutString("hello")
	e.PutBlob([]byte{0xCA, 0xFE})

	d := NewDecoder(e.Bytes())

	v8, err := d.Uint8()
	if err != nil {
		t.Fatal(err)
	}
	if v8 != 0x42 {
		t.Errorf("uint8 = 0x%02X, want 0x42", v8)
	}

	v16, err := d.Uint16()
	if err != nil {
		t.Fatal(err)
	}
	if v16 != 0x1234 {
		t.Errorf("uint16 = 0x%04X, want 0x1234", v16)
	}

	v32, err := d.Uint32()
	if err != nil {
		t.Fatal(err)
	}
	if v32 != 0xDEADBEEF {
		t.Errorf("uint32 = 0x%08X, want 0xDEADBEEF", v32)
	}

	v64, err := d.Uint64()
	if err != nil {
		t.Fatal(err)
	}
	if v64 != 0x0102030405060708 {
		t.Errorf("uint64 = 0x%016X, want 0x0102030405060708", v64)
	}

	guid, err := d.GUID()
	if err != nil {
		t.Fatal(err)
	}
	expected := GUID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	if guid != expected {
		t.Errorf("GUID = %x, want %x", guid, expected)
	}

	s, err := d.String()
	if err != nil {
		t.Fatal(err)
	}
	if s != "hello" {
		t.Errorf("string = %q, want %q", s, "hello")
	}

	blob, err := d.Blob()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(blob, []byte{0xCA, 0xFE}) {
		t.Errorf("blob = %x, want CAFE", blob)
	}

	if d.Remaining() != 0 {
		t.Errorf("remaining = %d, want 0", d.Remaining())
	}
}

func TestDecoderShortRead(t *testing.T) {
	d := NewDecoder([]byte{0x01})

	_, err := d.Uint16()
	if err == nil {
		t.Error("expected error for short uint16 read")
	}

	d2 := NewDecoder([]byte{0x05, 0x00, 0x00, 0x00, 'h'}) // string len=5, only 1 byte
	_, err = d2.String()
	if err == nil {
		t.Error("expected error for truncated string")
	}
}

func TestRequestHeaderRoundTrip(t *testing.T) {
	// Build a minimal request: header only, no payload.
	var buf bytes.Buffer
	msg := make([]byte, RequestHeaderSize)
	binary.LittleEndian.PutUint32(msg[0:4], RequestHeaderSize)  // total_len
	binary.LittleEndian.PutUint64(msg[4:12], 42)                // request_id
	binary.LittleEndian.PutUint16(msg[12:14], OpLookup)         // op_code
	binary.LittleEndian.PutUint64(msg[14:22], 0)                // txn_id
	buf.Write(msg)

	hdr, payload, err := ReadRequestHeader(&buf)
	if err != nil {
		t.Fatalf("ReadRequestHeader: %v", err)
	}
	if hdr.TotalLen != RequestHeaderSize {
		t.Errorf("TotalLen = %d, want %d", hdr.TotalLen, RequestHeaderSize)
	}
	if hdr.RequestID != 42 {
		t.Errorf("RequestID = %d, want 42", hdr.RequestID)
	}
	if hdr.OpCode != OpLookup {
		t.Errorf("OpCode = 0x%04X, want 0x%04X", hdr.OpCode, OpLookup)
	}
	if hdr.TxnID != 0 {
		t.Errorf("TxnID = %d, want 0", hdr.TxnID)
	}
	if len(payload) != 0 {
		t.Errorf("payload len = %d, want 0", len(payload))
	}
}

func TestRequestHeaderWithPayload(t *testing.T) {
	// Build a request with a 10-byte payload.
	payloadData := []byte("0123456789")
	totalLen := uint32(RequestHeaderSize + len(payloadData))

	var buf bytes.Buffer
	msg := make([]byte, totalLen)
	binary.LittleEndian.PutUint32(msg[0:4], totalLen)
	binary.LittleEndian.PutUint64(msg[4:12], 99)
	binary.LittleEndian.PutUint16(msg[12:14], OpSetValue)
	binary.LittleEndian.PutUint64(msg[14:22], 7) // txn_id
	copy(msg[22:], payloadData)
	buf.Write(msg)

	hdr, payload, err := ReadRequestHeader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.RequestID != 99 {
		t.Errorf("RequestID = %d, want 99", hdr.RequestID)
	}
	if hdr.TxnID != 7 {
		t.Errorf("TxnID = %d, want 7", hdr.TxnID)
	}
	if !bytes.Equal(payload, payloadData) {
		t.Errorf("payload = %q, want %q", payload, payloadData)
	}
}

func TestRequestHeaderTooSmall(t *testing.T) {
	// total_len < RequestHeaderSize
	var buf bytes.Buffer
	msg := make([]byte, RequestHeaderSize)
	binary.LittleEndian.PutUint32(msg[0:4], 10) // too small
	buf.Write(msg)

	_, _, err := ReadRequestHeader(&buf)
	if err == nil {
		t.Error("expected error for total_len < header size")
	}
}

func TestRequestHeaderWithTrailingBytes(t *testing.T) {
	// Simulate a future protocol version that appends extra fields
	// after the known payload. The total_len accounts for the extra
	// bytes, and the dispatcher passes them as part of the payload.
	// Decoders must silently ignore trailing bytes.
	knownPayload := []byte("0123456789")
	trailingBytes := []byte{0xFF, 0xFE, 0xFD} // unknown future fields
	fullPayload := append(knownPayload, trailingBytes...)
	totalLen := uint32(RequestHeaderSize + len(fullPayload))

	var buf bytes.Buffer
	msg := make([]byte, totalLen)
	binary.LittleEndian.PutUint32(msg[0:4], totalLen)
	binary.LittleEndian.PutUint64(msg[4:12], 55)
	binary.LittleEndian.PutUint16(msg[12:14], OpSetValue)
	binary.LittleEndian.PutUint64(msg[14:22], 0)
	copy(msg[22:], fullPayload)
	buf.Write(msg)

	hdr, payload, err := ReadRequestHeader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if hdr.RequestID != 55 {
		t.Errorf("RequestID = %d, want 55", hdr.RequestID)
	}
	// The full payload (including trailing bytes) must be delivered.
	if !bytes.Equal(payload, fullPayload) {
		t.Errorf("payload = %x, want %x", payload, fullPayload)
	}
	// The trailing bytes must be accessible.
	if len(payload) != len(knownPayload)+len(trailingBytes) {
		t.Errorf("payload length = %d, want %d", len(payload), len(knownPayload)+len(trailingBytes))
	}
}

func TestRequestHeaderExceedsMaxSize(t *testing.T) {
	var buf bytes.Buffer
	msg := make([]byte, RequestHeaderSize)
	// Set total_len to something absurdly large.
	binary.LittleEndian.PutUint32(msg[0:4], MaxRequestSize+1)
	buf.Write(msg)

	_, _, err := ReadRequestHeader(&buf)
	if err == nil {
		t.Error("expected error for oversized total_len")
	}
}

func TestWriteResponse(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte{0xAA, 0xBB}
	err := WriteResponse(&buf, 42, OpLookup, StatusOK, payload)
	if err != nil {
		t.Fatal(err)
	}

	data := buf.Bytes()
	expectedLen := uint32(ResponseHeaderSize + 4 + len(payload)) // 14 + 4 + 2 = 20

	totalLen := binary.LittleEndian.Uint32(data[0:4])
	if totalLen != expectedLen {
		t.Errorf("total_len = %d, want %d", totalLen, expectedLen)
	}

	requestID := binary.LittleEndian.Uint64(data[4:12])
	if requestID != 42 {
		t.Errorf("request_id = %d, want 42", requestID)
	}

	opCode := binary.LittleEndian.Uint16(data[12:14])
	if opCode != OpLookup|ResponseBit {
		t.Errorf("op_code = 0x%04X, want 0x%04X", opCode, OpLookup|ResponseBit)
	}

	status := binary.LittleEndian.Uint32(data[14:18])
	if status != StatusOK {
		t.Errorf("status = %d, want %d", status, StatusOK)
	}

	if !bytes.Equal(data[18:], payload) {
		t.Errorf("payload = %x, want %x", data[18:], payload)
	}
}

func TestWriteErrorResponse(t *testing.T) {
	var buf bytes.Buffer
	err := WriteErrorResponse(&buf, 7, OpCreateKey, StatusStorageError)
	if err != nil {
		t.Fatal(err)
	}

	data := buf.Bytes()
	expectedLen := uint32(ResponseHeaderSize + 4) // 18 bytes

	totalLen := binary.LittleEndian.Uint32(data[0:4])
	if totalLen != expectedLen {
		t.Errorf("total_len = %d, want %d", totalLen, expectedLen)
	}

	status := binary.LittleEndian.Uint32(data[14:18])
	if status != StatusStorageError {
		t.Errorf("status = %d, want %d", status, StatusStorageError)
	}

	if len(data) != int(expectedLen) {
		t.Errorf("response length = %d, want %d", len(data), expectedLen)
	}
}
