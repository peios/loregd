package rsi

import (
	"encoding/binary"
	"fmt"
	"io"
)

// GUID is a 16-byte identifier.
type GUID [16]byte

// Encoder writes binary RSI data to a buffer.
type Encoder struct {
	buf []byte
}

// NewEncoder creates an encoder with the given initial capacity.
func NewEncoder(cap int) *Encoder {
	return &Encoder{buf: make([]byte, 0, cap)}
}

func (e *Encoder) PutUint8(v uint8)   { e.buf = append(e.buf, v) }
func (e *Encoder) PutUint16(v uint16) { e.buf = binary.LittleEndian.AppendUint16(e.buf, v) }
func (e *Encoder) PutUint32(v uint32) { e.buf = binary.LittleEndian.AppendUint32(e.buf, v) }
func (e *Encoder) PutUint64(v uint64) { e.buf = binary.LittleEndian.AppendUint64(e.buf, v) }
func (e *Encoder) PutGUID(g GUID)     { e.buf = append(e.buf, g[:]...) }
func (e *Encoder) PutBytes(b []byte)  { e.buf = append(e.buf, b...) }

// PutBool writes a boolean as one byte (0 = false, 1 = true).
func (e *Encoder) PutBool(v bool) {
	if v {
		e.PutUint8(1)
		return
	}
	e.PutUint8(0)
}

// PutString writes a length-prefixed string (uint32 len + UTF-8 bytes).
func (e *Encoder) PutString(s string) {
	e.PutUint32(uint32(len(s)))
	e.buf = append(e.buf, s...)
}

// PutBlob writes a length-prefixed byte array (uint32 len + bytes).
func (e *Encoder) PutBlob(b []byte) {
	e.PutUint32(uint32(len(b)))
	e.buf = append(e.buf, b...)
}

// Bytes returns the encoded data.
func (e *Encoder) Bytes() []byte { return e.buf }

// Len returns the current length of the encoded data.
func (e *Encoder) Len() int { return len(e.buf) }

// Decoder reads binary RSI data from a byte slice.
type Decoder struct {
	buf []byte
	off int
}

// NewDecoder creates a decoder over the given byte slice.
func NewDecoder(buf []byte) *Decoder {
	return &Decoder{buf: buf}
}

// Remaining returns the number of unread bytes.
func (d *Decoder) Remaining() int { return len(d.buf) - d.off }

func (d *Decoder) need(n int) error {
	if d.off+n > len(d.buf) {
		return fmt.Errorf("short read: need %d bytes, have %d", n, d.Remaining())
	}
	return nil
}

func (d *Decoder) Uint8() (uint8, error) {
	if err := d.need(1); err != nil {
		return 0, err
	}
	v := d.buf[d.off]
	d.off++
	return v, nil
}

// Bool reads a one-byte boolean. Any non-zero value is treated as true.
func (d *Decoder) Bool() (bool, error) {
	v, err := d.Uint8()
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

func (d *Decoder) Uint16() (uint16, error) {
	if err := d.need(2); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint16(d.buf[d.off:])
	d.off += 2
	return v, nil
}

func (d *Decoder) Uint32() (uint32, error) {
	if err := d.need(4); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint32(d.buf[d.off:])
	d.off += 4
	return v, nil
}

func (d *Decoder) Uint64() (uint64, error) {
	if err := d.need(8); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint64(d.buf[d.off:])
	d.off += 8
	return v, nil
}

func (d *Decoder) GUID() (GUID, error) {
	var g GUID
	if err := d.need(16); err != nil {
		return g, err
	}
	copy(g[:], d.buf[d.off:d.off+16])
	d.off += 16
	return g, nil
}

// String reads a length-prefixed string.
func (d *Decoder) String() (string, error) {
	n, err := d.Uint32()
	if err != nil {
		return "", err
	}
	if err := d.need(int(n)); err != nil {
		return "", err
	}
	s := string(d.buf[d.off : d.off+int(n)])
	d.off += int(n)
	return s, nil
}

// Blob reads a length-prefixed byte array.
func (d *Decoder) Blob() ([]byte, error) {
	n, err := d.Uint32()
	if err != nil {
		return nil, err
	}
	if err := d.need(int(n)); err != nil {
		return nil, err
	}
	b := make([]byte, n)
	copy(b, d.buf[d.off:d.off+int(n)])
	d.off += int(n)
	return b, nil
}

// RequestHeader is the common header for all RSI requests.
type RequestHeader struct {
	TotalLen  uint32
	RequestID uint64
	OpCode    uint16
	TxnID     uint64
}

// ReadRequestHeader reads a complete request from a reader.
// Returns the header and the payload (everything after the header).
func ReadRequestHeader(r io.Reader) (RequestHeader, []byte, error) {
	// Read total_len first.
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return RequestHeader{}, nil, fmt.Errorf("read total_len: %w", err)
	}
	totalLen := binary.LittleEndian.Uint32(lenBuf[:])
	if totalLen < RequestHeaderSize {
		return RequestHeader{}, nil, fmt.Errorf("total_len %d < minimum header size %d", totalLen, RequestHeaderSize)
	}
	if totalLen > MaxRequestSize {
		return RequestHeader{}, nil, fmt.Errorf("total_len %d exceeds maximum request size %d", totalLen, MaxRequestSize)
	}

	// Read the rest of the message.
	msg := make([]byte, totalLen)
	copy(msg[:4], lenBuf[:])
	if _, err := io.ReadFull(r, msg[4:]); err != nil {
		return RequestHeader{}, nil, fmt.Errorf("read message body: %w", err)
	}

	hdr := RequestHeader{
		TotalLen:  totalLen,
		RequestID: binary.LittleEndian.Uint64(msg[4:12]),
		OpCode:    binary.LittleEndian.Uint16(msg[12:14]),
		TxnID:     binary.LittleEndian.Uint64(msg[14:22]),
	}

	return hdr, msg[RequestHeaderSize:], nil
}

// ParseRequest parses a single complete in-memory RSI request message.
//
// Unlike ReadRequestHeader (which streams from an io.Reader), this
// operates on one fully-read message buffer, as delivered by the
// message-oriented /dev/pkm_registry device where one read() returns
// exactly one request. It validates that total_len frames the buffer
// exactly; a mismatch is malformed framing (the caller tears down the
// connection per PSD-005 §7.1).
func ParseRequest(msg []byte) (RequestHeader, []byte, error) {
	if len(msg) < RequestHeaderSize {
		return RequestHeader{}, nil, fmt.Errorf("message %d bytes < minimum header size %d", len(msg), RequestHeaderSize)
	}
	totalLen := binary.LittleEndian.Uint32(msg[0:4])
	if totalLen > MaxRequestSize {
		return RequestHeader{}, nil, fmt.Errorf("total_len %d exceeds maximum request size %d", totalLen, MaxRequestSize)
	}
	if int(totalLen) != len(msg) {
		return RequestHeader{}, nil, fmt.Errorf("total_len %d does not match message length %d (malformed framing)", totalLen, len(msg))
	}

	hdr := RequestHeader{
		TotalLen:  totalLen,
		RequestID: binary.LittleEndian.Uint64(msg[4:12]),
		OpCode:    binary.LittleEndian.Uint16(msg[12:14]),
		TxnID:     binary.LittleEndian.Uint64(msg[14:22]),
	}
	return hdr, msg[RequestHeaderSize:], nil
}

// WriteResponse writes a complete response to a writer.
func WriteResponse(w io.Writer, requestID uint64, opCode uint16, status uint32, payload []byte) error {
	totalLen := uint32(ResponseHeaderSize + 4 + len(payload)) // header + status + payload
	buf := make([]byte, 0, totalLen)

	// Response header.
	buf = binary.LittleEndian.AppendUint32(buf, totalLen)
	buf = binary.LittleEndian.AppendUint64(buf, requestID)
	buf = binary.LittleEndian.AppendUint16(buf, opCode|ResponseBit)

	// Status.
	buf = binary.LittleEndian.AppendUint32(buf, status)

	// Payload.
	buf = append(buf, payload...)

	_, err := w.Write(buf)
	return err
}

// WriteErrorResponse writes a response with only a status code (no payload).
func WriteErrorResponse(w io.Writer, requestID uint64, opCode uint16, status uint32) error {
	return WriteResponse(w, requestID, opCode, status, nil)
}
