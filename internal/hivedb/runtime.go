package hivedb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/peios/peios-go/file"
	"github.com/peios/peios-go/security"
	"golang.org/x/sys/unix"
)

// RuntimeStore owns one daemon's volatile files. The directory lock survives
// until all hive connections close. A replacement daemon removes crash residue
// only after obtaining that lock; it never opens an old volatile database.
type RuntimeStore struct {
	lock *os.File
	Path string
}

type runtimeProtection bool

const (
	posixOnlyProtection   runtimeProtection = false // For host-side storage tests only.
	nativeProtection      runtimeProtection = true
	runtimeDescriptorSDDL                   = "O:SYG:SYD:P(A;OICI;GA;;;SY)"
)

// OpenRuntimeStore locks and protects a tmpfs directory for this daemon's lifetime.
// The caller must close every hive before releasing the returned store's lock.
func OpenRuntimeStore(path string) (*RuntimeStore, error) {
	return openRuntimeStore(path, nativeProtection)
}

func openRuntimeStore(path string, protection runtimeProtection) (*RuntimeStore, error) {
	if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), path)
	defer func() { _ = directory.Close() }() // Read-only directory, not the lifetime lock.
	if protection == nativeProtection {
		if err := protectRuntimeDirectory(directory); err != nil {
			return nil, err
		}
	}
	lock, err := acquireRuntimeLock(path)
	if err != nil {
		return nil, err
	}
	if err := cleanupRuntimeResidue(directory, path); err != nil {
		_ = lock.Close() // Preserve the cleanup failure; no store is returned.
		return nil, err
	}
	return &RuntimeStore{lock: lock, Path: path}, nil
}

func protectRuntimeDirectory(directory *os.File) error {
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(int(directory.Fd()), &filesystem); err != nil {
		return err
	}
	if filesystem.Type != unix.TMPFS_MAGIC {
		return errors.New("volatile runtime directory must be on tmpfs")
	}
	// Bootstrap runs as SYSTEM. Apply native authority to the opened directory
	// before creating files; POSIX mode alone is insufficient on Peios.
	descriptor, err := security.ParseSDDL(runtimeDescriptorSDDL)
	if err != nil {
		return err
	}
	blob, err := descriptor.MarshalBinary()
	if err != nil {
		return err
	}
	return file.SetDescriptor(
		directory, security.InfoOwner|security.InfoGroup|security.InfoDACL, blob,
	)
}

func acquireRuntimeLock(path string) (*os.File, error) {
	// FACS requires write authority for an exclusive flock. A read-only directory
	// cannot carry it, so use a stable inherited lock file.
	lockPath := filepath.Join(path, ".lock")
	flags := unix.O_RDWR | unix.O_CREAT | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Open(lockPath, flags, 0o600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close() // Preserve the lock-acquisition failure.
		return nil, fmt.Errorf("runtime directory in use: %w", err)
	}
	return lock, nil
}

// cleanupRuntimeResidue runs only while holding the directory's lifetime lock.
// The protected directory contains only per-hive stores owned by loregd.
func cleanupRuntimeResidue(directory *os.File, path string) error {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == ".lock" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// Close releases the runtime lock after the caller has closed every hive.
func (s *RuntimeStore) Close() error { return s.lock.Close() }
