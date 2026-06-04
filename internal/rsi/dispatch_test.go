package rsi

import (
	"bytes"
	"encoding/binary"
	"io"
	"sync"
	"sync/atomic"
	"testing"
)

// buildRequest constructs a complete wire request.
func buildRequest(requestID uint64, opCode uint16, txnID uint64, payload []byte) []byte {
	totalLen := uint32(RequestHeaderSize + len(payload))
	buf := make([]byte, totalLen)
	binary.LittleEndian.PutUint32(buf[0:4], totalLen)
	binary.LittleEndian.PutUint64(buf[4:12], requestID)
	binary.LittleEndian.PutUint16(buf[12:14], opCode)
	binary.LittleEndian.PutUint64(buf[14:22], txnID)
	copy(buf[22:], payload)
	return buf
}

// parseResponse reads a complete response from a buffer.
func parseResponse(data []byte) (requestID uint64, opCode uint16, status uint32, payload []byte) {
	totalLen := binary.LittleEndian.Uint32(data[0:4])
	requestID = binary.LittleEndian.Uint64(data[4:12])
	opCode = binary.LittleEndian.Uint16(data[12:14])
	status = binary.LittleEndian.Uint32(data[14:18])
	if totalLen > ResponseHeaderSize+4 {
		payload = data[18:totalLen]
	}
	return
}

func TestDispatchKnownHandler(t *testing.T) {
	d := NewDispatcher()
	var called atomic.Bool
	d.Register(OpFlush, func(hdr RequestHeader, payload []byte) (uint32, []byte) {
		called.Store(true)
		if hdr.RequestID != 7 {
			t.Errorf("handler got RequestID=%d, want 7", hdr.RequestID)
		}
		return StatusOK, nil
	})

	req := buildRequest(7, OpFlush, 0, nil)
	r := bytes.NewReader(req)
	var w bytes.Buffer

	err := d.Serve(r, &w)
	if err == nil || err.Error() == "" {
		// Serve returns error when reader is exhausted (EOF).
	}

	if !called.Load() {
		t.Error("handler was not called")
	}

	if w.Len() == 0 {
		t.Fatal("no response written")
	}

	rid, oc, status, _ := parseResponse(w.Bytes())
	if rid != 7 {
		t.Errorf("response request_id = %d, want 7", rid)
	}
	if oc != OpFlush|ResponseBit {
		t.Errorf("response op_code = 0x%04X, want 0x%04X", oc, OpFlush|ResponseBit)
	}
	if status != StatusOK {
		t.Errorf("response status = %d, want %d", status, StatusOK)
	}
}

func TestDispatchUnknownOpCode(t *testing.T) {
	d := NewDispatcher()
	req := buildRequest(1, 0xFF, 0, nil)
	r := bytes.NewReader(req)
	var w bytes.Buffer

	d.Serve(r, &w)

	if w.Len() == 0 {
		t.Fatal("no response for unknown op_code")
	}

	_, _, status, _ := parseResponse(w.Bytes())
	if status != StatusInvalid {
		t.Errorf("status = %d, want %d (StatusInvalid)", status, StatusInvalid)
	}
}

func TestDispatchMultipleRequests(t *testing.T) {
	d := NewDispatcher()
	var count atomic.Int32
	d.Register(OpReadKey, func(hdr RequestHeader, payload []byte) (uint32, []byte) {
		count.Add(1)
		return StatusOK, nil
	})

	var input bytes.Buffer
	for i := uint64(0); i < 5; i++ {
		input.Write(buildRequest(i, OpReadKey, 0, nil))
	}

	var w bytes.Buffer
	d.Serve(&input, &w)

	if count.Load() != 5 {
		t.Errorf("handler called %d times, want 5", count.Load())
	}
}

func TestDispatchConcurrency(t *testing.T) {
	d := NewDispatcher()
	var mu sync.Mutex
	seen := make(map[uint64]bool)

	d.Register(OpQueryValues, func(hdr RequestHeader, payload []byte) (uint32, []byte) {
		mu.Lock()
		seen[hdr.RequestID] = true
		mu.Unlock()
		return StatusOK, nil
	})

	// Use a pipe to allow concurrent reads.
	pr, pw := io.Pipe()
	var w bytes.Buffer

	go func() {
		for i := uint64(0); i < 10; i++ {
			pw.Write(buildRequest(i, OpQueryValues, 0, nil))
		}
		pw.Close()
	}()

	d.Serve(pr, &w)

	mu.Lock()
	if len(seen) != 10 {
		t.Errorf("saw %d unique requests, want 10", len(seen))
	}
	mu.Unlock()
}

func TestDispatchHandlerPayload(t *testing.T) {
	d := NewDispatcher()
	d.Register(OpFlush, func(hdr RequestHeader, payload []byte) (uint32, []byte) {
		// Decode the flush request and echo the hive name in the response.
		req, err := DecodeFlushRequest(NewDecoder(payload))
		if err != nil {
			return StatusInvalid, nil
		}
		e := NewEncoder(32)
		e.PutString(req.HiveName)
		return StatusOK, e.Bytes()
	})

	reqPayload := buildPayload(func(e *Encoder) {
		e.PutString("Machine")
	})
	req := buildRequest(1, OpFlush, 0, reqPayload)

	var w bytes.Buffer
	d.Serve(bytes.NewReader(req), &w)

	_, _, status, respPayload := parseResponse(w.Bytes())
	if status != StatusOK {
		t.Fatalf("status = %d", status)
	}

	rd := NewDecoder(respPayload)
	name, err := rd.String()
	if err != nil {
		t.Fatal(err)
	}
	if name != "Machine" {
		t.Errorf("echoed name = %q, want %q", name, "Machine")
	}
}
