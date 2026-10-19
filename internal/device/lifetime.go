package device

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/peios/loregd/internal/rsi"
)

// Version 1 adds out-of-band lifetime control; RSI data frames are unchanged.
const (
	RegSrcNegotiate      = 0xc0105201
	RegSrcRequestContext = 0xc0285202
	RegSrcRenew          = 0xc0205203
)

// Lifetime owns the negotiated session identity and borrows the open device.
// The device must remain open while requests or lease renewals use this session.
type Lifetime struct {
	file    *os.File
	session uint64
}

func (l *Lifetime) ioctl(command uintptr, data unsafe.Pointer) error {
	raw, err := l.file.SyscallConn()
	if err != nil {
		return err
	}
	var errno unix.Errno
	err = raw.Control(func(fd uintptr) {
		// Control keeps the descriptor live; data points to the caller's live ioctl structure.
		_, _, errno = unix.Syscall(unix.SYS_IOCTL, fd, command, uintptr(data))
	})
	if err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	return nil
}

// Negotiate must precede hive registration. Unsupported kernels are rejected:
// silently reverting to a guessed TTL would violate transaction ownership.
func Negotiate(file *os.File) (*Lifetime, error) {
	l := &Lifetime{file: file}
	args := struct {
		Version, Flags uint32
		Session        uint64
	}{Version: 1}
	if err := l.ioctl(RegSrcNegotiate, unsafe.Pointer(&args)); err != nil {
		return nil, fmt.Errorf("negotiate RSI lifetime v1: %w", err)
	}
	if args.Version != 1 || args.Flags != 0 || args.Session == 0 {
		return nil, errors.New("invalid lifetime negotiation")
	}
	l.session = args.Session
	return l, nil
}

// Context fills a request's absolute monotonic deadlines from its kernel-owned lifetime.
func (l *Lifetime) Context(hdr *rsi.RequestHeader) error {
	args := struct {
		Session, RequestID                               uint64
		RequestDeadline, LifetimeDeadline, LeaseDeadline uint64
	}{Session: l.session, RequestID: hdr.RequestID}
	if err := l.ioctl(RegSrcRequestContext, unsafe.Pointer(&args)); err != nil {
		return err
	}
	hdr.Session = l.session
	hdr.DeadlineNS = args.RequestDeadline
	hdr.LifetimeNS = args.LifetimeDeadline
	hdr.LeaseNS = args.LeaseDeadline
	return nil
}

// Renew extends an active transaction lease in this session and returns its deadline.
// A mismatched session is rejected without issuing an ioctl.
func (l *Lifetime) Renew(session, id uint64) (uint64, error) {
	if session != l.session {
		return 0, unix.ESTALE
	}
	args := struct {
		Session, TransactionID          uint64
		LifetimeDeadline, LeaseDeadline uint64
	}{Session: session, TransactionID: id}
	if err := l.ioctl(RegSrcRenew, unsafe.Pointer(&args)); err != nil {
		return 0, err
	}
	return args.LeaseDeadline, nil
}
