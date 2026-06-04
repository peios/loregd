// Package device implements loregd's boundary against the LCS
// /dev/pkm_registry character device: opening the device, the
// REG_SRC_REGISTER handshake, and the message-oriented RSI request
// loop (PSD-006 §2 startup steps 8-10, PSD-005 §7).
//
// The device is binary and message-oriented: one read() returns
// exactly one complete RSI request, and one write() submits exactly
// one complete response (PSD-005 §7.1). This package therefore reads
// whole messages into a buffer rather than streaming, and lets the
// rsi.Dispatcher serialize responses.
package device

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/peios/loregd/internal/rsi"
)

// Path is the LCS source device. open() requires SeTcbPrivilege in the
// calling thread's token; the kernel enforces this (PSD-005 §7.4).
const Path = "/dev/pkm_registry"

// REG_SRC_REGISTER is the ioctl request number for source registration
// on a /dev/pkm_registry fd: _IOW('R', 0, sizeof(reg_src_register_args)).
//
// Encoding: dir(_IOC_WRITE=1)<<30 | size(24)<<16 | type('R'=0x52)<<8 | nr(0)
// = 0x40185200. Cross-checked against pkm/uapi (REG_SRC_REGISTER) in
// device_test.go so loregd stays byte-compatible with the kernel ABI.
const REG_SRC_REGISTER = 0x40185200

// On-wire struct sizes (PSD-005 §11, reg_src_register_args /
// reg_src_hive_entry). Little-endian; pointers are 64-bit.
const (
	regSrcRegisterArgsSize = 24 // hive_count(4) _pad(4) max_sequence(8) hives_ptr(8)
	regSrcHiveEntrySize    = 56 // name_len(4) _pad0(4) name_ptr(8) root_guid(16) flags(4) _pad1(4) scope_guid(16)
)

// HiveRegistration is one hive to register with LCS. loregd registers
// only global hives, so no PRIVATE flag or scope GUID is carried
// (PSD-006 §2 step 9).
type HiveRegistration struct {
	Name     string
	RootGUID [16]byte
}

// Open opens the LCS source device for reading and writing. Returns an
// error if the device is absent (not a Peios kernel) or the caller
// lacks SeTcbPrivilege (EPERM from the kernel open handler).
func Open() (*os.File, error) {
	f, err := os.OpenFile(Path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", Path, err)
	}
	return f, nil
}

// Register issues the REG_SRC_REGISTER ioctl, declaring all hives with
// their root GUIDs and the global maximum persisted sequence number
// (PSD-006 §2 step 9). On a non-zero errno it returns the wrapped
// errno (e.g. EPERM, EEXIST, ENOSPC, EOVERFLOW, ESTALE per PSD-005 §7).
func Register(fd uintptr, hives []HiveRegistration, maxSequence uint64) error {
	if len(hives) == 0 {
		return errors.New("register: no hives")
	}

	// Backing storage for hive-name strings. The kernel reads these
	// through name_ptr during the ioctl, so they MUST stay live (and
	// at a stable address) until the syscall returns; runtime.KeepAlive
	// below guarantees that.
	nameBufs := make([][]byte, len(hives))
	entries := make([]byte, len(hives)*regSrcHiveEntrySize)

	for i, hv := range hives {
		nb := []byte(hv.Name)
		nameBufs[i] = nb

		base := i * regSrcHiveEntrySize
		binary.LittleEndian.PutUint32(entries[base+0:], uint32(len(nb))) // name_len
		// entries[base+4:] _pad0 stays zero.
		var namePtr uint64
		if len(nb) > 0 {
			namePtr = uint64(uintptr(unsafe.Pointer(&nb[0])))
		}
		binary.LittleEndian.PutUint64(entries[base+8:], namePtr) // name_ptr
		copy(entries[base+16:base+32], hv.RootGUID[:])           // root_guid
		// flags (base+32) = 0 (global hive), _pad1 and scope_guid stay zero.
	}

	var args [regSrcRegisterArgsSize]byte
	binary.LittleEndian.PutUint32(args[0:], uint32(len(hives)))                            // hive_count
	binary.LittleEndian.PutUint64(args[8:], maxSequence)                                   // max_sequence
	binary.LittleEndian.PutUint64(args[16:], uint64(uintptr(unsafe.Pointer(&entries[0])))) // hives_ptr

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, uintptr(REG_SRC_REGISTER), uintptr(unsafe.Pointer(&args[0])))
	// Keep all kernel-referenced memory alive across the syscall.
	runtime.KeepAlive(nameBufs)
	runtime.KeepAlive(entries)
	runtime.KeepAlive(&args)

	if errno != 0 {
		return fmt.Errorf("REG_SRC_REGISTER: %w", errno)
	}
	return nil
}

// Serve runs the RSI request loop: read one complete request per read(),
// validate its framing, and dispatch it. Requests are processed
// concurrently (LCS multiplexes up to MaxConcurrentRSIRequests in
// flight); the dispatcher serializes responses, and each response is
// written with a single Write (one write() = one response).
//
// Serve blocks until the device returns an error: io.EOF on LCS
// shutdown / fd close (returned as nil), or a malformed-framing /
// I/O error (returned as the error, which tears the loop down).
func Serve(rw io.ReadWriter, d *rsi.Dispatcher) error {
	// Drain in-flight requests before returning so no handler writes to a
	// device or database that shutdown is about to close.
	var wg sync.WaitGroup
	defer wg.Wait()

	// One buffer per read; sized to the protocol maximum so a legitimate
	// request never triggers EMSGSIZE. Reused across reads — each message
	// is copied out before the next read overwrites it.
	buf := make([]byte, rsi.MaxRequestSize)

	for {
		n, err := rw.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read request: %w", err)
		}

		msg := make([]byte, n)
		copy(msg, buf[:n])

		hdr, payload, perr := rsi.ParseRequest(msg)
		if perr != nil {
			// Malformed framing: LCS tears down the connection (PSD-005
			// §7.1). We surface the error and stop serving.
			return fmt.Errorf("malformed request framing: %w", perr)
		}

		wg.Go(func() {
			d.Dispatch(rw, hdr, payload)
		})
	}
}
