package device

import (
	"bytes"
	"encoding/binary"
	"io"
	"sync"
	"testing"

	"github.com/peios/loregd/internal/rsi"
)

// TestRegSrcRegisterEncoding pins the ioctl request number and struct
// sizes to the kernel ABI (pkm/uapi: REG_SRC_REGISTER = 1075335680,
// REG_SRC_REGISTER_ARGS_SIZE = 24, REG_SRC_HIVE_ENTRY_SIZE = 56). If the
// kernel UAPI changes, this test must fail rather than loregd silently
// sending a malformed ioctl.
func TestRegSrcRegisterEncoding(t *testing.T) {
	const wantIoctl = 1075335680 // 0x40185200, from pkm/uapi/go zconst.go
	if REG_SRC_REGISTER != wantIoctl {
		t.Errorf("REG_SRC_REGISTER = %#x, want %#x", REG_SRC_REGISTER, wantIoctl)
	}

	// Independently derive the encoding: dir(_IOC_WRITE=1)<<30 |
	// size<<16 | type('R')<<8 | nr.
	const derived = (1 << 30) | (regSrcRegisterArgsSize << 16) | (0x52 << 8) // nr = 0
	if REG_SRC_REGISTER != derived {
		t.Errorf("REG_SRC_REGISTER = %#x, derived %#x", REG_SRC_REGISTER, derived)
	}

	if regSrcRegisterArgsSize != 24 {
		t.Errorf("regSrcRegisterArgsSize = %d, want 24", regSrcRegisterArgsSize)
	}
	if regSrcHiveEntrySize != 56 {
		t.Errorf("regSrcHiveEntrySize = %d, want 56", regSrcHiveEntrySize)
	}
}

// errReader returns an error after delivering a fixed set of messages.
type scriptReader struct {
	msgs [][]byte
	idx  int
	err  error
}

func (r *scriptReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.msgs) {
		return 0, r.err
	}
	m := r.msgs[r.idx]
	r.idx++
	n := copy(p, m)
	return n, nil
}

// rw pairs a scriptReader with a synchronized buffer for responses.
type rw struct {
	r  io.Reader
	mu sync.Mutex
	w  bytes.Buffer
}

func (x *rw) Read(p []byte) (int, error) { return x.r.Read(p) }
func (x *rw) Write(p []byte) (int, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.w.Write(p)
}

func frameRequest(reqID uint64, op uint16, txn uint64, payload []byte) []byte {
	total := rsi.RequestHeaderSize + len(payload)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:], uint32(total))
	binary.LittleEndian.PutUint64(buf[4:], reqID)
	binary.LittleEndian.PutUint16(buf[12:], op)
	binary.LittleEndian.PutUint64(buf[14:], txn)
	copy(buf[rsi.RequestHeaderSize:], payload)
	return buf
}

// TestServeDispatchesAndResponds drives one request through Serve and
// checks that a framed response comes back. It uses a stub handler so
// the test does not depend on the SQLite-backed handlers.
func TestServeDispatchesAndResponds(t *testing.T) {
	const op = uint16(0x01)
	done := make(chan struct{})
	disp := rsi.NewDispatcher()
	disp.Register(op, func(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
		defer close(done)
		if hdr.RequestID != 42 {
			t.Errorf("request_id = %d, want 42", hdr.RequestID)
		}
		return rsi.StatusOK, nil
	})

	conn := &rw{r: &scriptReader{
		msgs: [][]byte{frameRequest(42, op, 0, nil)},
		err:  io.EOF,
	}}

	if err := Serve(conn, disp); err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}
	<-done

	conn.mu.Lock()
	resp := conn.w.Bytes()
	conn.mu.Unlock()

	if len(resp) < rsi.ResponseHeaderSize+4 {
		t.Fatalf("response too short: %d bytes", len(resp))
	}
	gotReqID := binary.LittleEndian.Uint64(resp[4:12])
	gotOp := binary.LittleEndian.Uint16(resp[12:14])
	gotStatus := binary.LittleEndian.Uint32(resp[14:18])
	if gotReqID != 42 {
		t.Errorf("response request_id = %d, want 42", gotReqID)
	}
	if gotOp != op|rsi.ResponseBit {
		t.Errorf("response op_code = %#x, want %#x", gotOp, op|rsi.ResponseBit)
	}
	if gotStatus != rsi.StatusOK {
		t.Errorf("response status = %d, want %d", gotStatus, rsi.StatusOK)
	}
}

// TestServeMalformedFramingTearsDown verifies that a total_len that does
// not match the delivered message length is treated as malformed framing
// and stops the loop (PSD-005 §7.1).
func TestServeMalformedFramingTearsDown(t *testing.T) {
	// total_len claims 100 but message is short.
	bad := make([]byte, 30)
	binary.LittleEndian.PutUint32(bad[0:], 100)

	conn := &rw{r: &scriptReader{msgs: [][]byte{bad}, err: io.EOF}}
	disp := rsi.NewDispatcher()

	err := Serve(conn, disp)
	if err == nil {
		t.Fatal("expected error on malformed framing, got nil")
	}
}

// TestServeCleanEOF verifies that an immediate EOF (LCS shutdown / device
// close) returns nil, not an error.
func TestServeCleanEOF(t *testing.T) {
	conn := &rw{r: &scriptReader{err: io.EOF}}
	disp := rsi.NewDispatcher()
	if err := Serve(conn, disp); err != nil {
		t.Fatalf("Serve on clean EOF = %v, want nil", err)
	}
}
