package rsi

import (
	"fmt"
	"io"
	"log"
	"sync"
)

// Handler processes an RSI request and returns a status code and
// optional response payload. Handlers are registered per op_code.
type Handler func(hdr RequestHeader, payload []byte) (status uint32, response []byte)

// Dispatcher reads RSI requests from a reader, dispatches them to
// registered handlers, and writes responses to a writer.
type Dispatcher struct {
	handlers map[uint16]Handler
	mu       sync.Mutex // serializes writes
}

// NewDispatcher creates a dispatcher with no registered handlers.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		handlers: make(map[uint16]Handler),
	}
}

// Register adds a handler for the given op_code.
func (d *Dispatcher) Register(opCode uint16, h Handler) {
	d.handlers[opCode] = h
}

// HasHandler reports whether a handler is registered for opCode.
func (d *Dispatcher) HasHandler(opCode uint16) bool {
	_, ok := d.handlers[opCode]
	return ok
}

// HandlerCount returns the number of registered operation handlers.
func (d *Dispatcher) HandlerCount() int {
	return len(d.handlers)
}

// Serve reads requests from r, dispatches them concurrently, and
// writes responses to w. It blocks until r returns an error (typically
// io.EOF on device close). Responses are serialized via a mutex.
func (d *Dispatcher) Serve(r io.Reader, w io.Writer) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		hdr, payload, err := ReadRequestHeader(r)
		if err != nil {
			return fmt.Errorf("read request: %w", err)
		}

		wg.Add(1)
		go func(hdr RequestHeader, payload []byte) {
			defer wg.Done()
			d.dispatch(w, hdr, payload)
		}(hdr, payload)
	}
}

// Dispatch processes one already-parsed request and writes its response
// to w. It is the entry point for the message-oriented device serve loop
// (where framing is parsed by the caller via ParseRequest). Responses are
// serialized via the dispatcher's write mutex, so Dispatch is safe to call
// concurrently from multiple goroutines sharing one writer.
func (d *Dispatcher) Dispatch(w io.Writer, hdr RequestHeader, payload []byte) {
	d.dispatch(w, hdr, payload)
}

func (d *Dispatcher) dispatch(w io.Writer, hdr RequestHeader, payload []byte) {
	h, ok := d.handlers[hdr.OpCode]
	if !ok {
		log.Printf("rsi: unknown op_code 0x%04X (request_id=%d)", hdr.OpCode, hdr.RequestID)
		d.writeResponse(w, hdr.RequestID, hdr.OpCode, StatusInvalid, nil)
		return
	}

	status, resp := h(hdr, payload)
	d.writeResponse(w, hdr.RequestID, hdr.OpCode, status, resp)
}

func (d *Dispatcher) writeResponse(w io.Writer, requestID uint64, opCode uint16, status uint32, payload []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := WriteResponse(w, requestID, opCode, status, payload); err != nil {
		log.Printf("rsi: write response failed (request_id=%d): %v", requestID, err)
	}
}
