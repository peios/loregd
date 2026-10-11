package hivedb

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/peios/libp-go/sd"
	"github.com/peios/libp-go/sddl"
	"golang.org/x/sys/unix"
)

// RuntimeStore owns one daemon's volatile files. The directory lock survives
// until all hive connections close. A replacement daemon removes crash residue
// only after obtaining that lock; it never opens an old volatile database.
type RuntimeStore struct {
	lock *os.File
	Path string
}

func OpenRuntimeStore(path string) (*RuntimeStore, error) {
	return openRuntimeStore(path, true)
}

func openRuntimeStore(path string, native bool) (*RuntimeStore, error) {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), path)
	defer dir.Close()
	ok := false
	if native {
		var fs unix.Statfs_t
		if err := unix.Fstatfs(fd, &fs); err != nil {
			return nil, err
		}
		if fs.Type != unix.TMPFS_MAGIC {
			return nil, fmt.Errorf("volatile runtime directory must be on tmpfs")
		}
		// loregd runs as SYSTEM during registry bootstrap. Apply native
		// authority before creating any files; POSIX mode alone is insufficient.
		d, err := sddl.Parse("O:SYG:SYD:P(A;OICI;GA;;;SY)")
		if err != nil {
			return nil, err
		}
		blob, err := d.Marshal()
		if err != nil {
			return nil, err
		}
		if err := sd.SetSD(sd.Path(path), sd.InfoOwner|sd.InfoGroup|sd.InfoDACL, blob); err != nil {
			return nil, err
		}
	}
	// FACS requires write authority for an exclusive flock. A directory
	// opened read-only cannot carry it; use a stable inherited lock file.
	lockFD, err := unix.Open(filepath.Join(path, ".lock"), unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(lockFD), filepath.Join(path, ".lock"))
	defer func() {
		if !ok {
			lock.Close()
		}
	}()
	if err := unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, fmt.Errorf("runtime directory in use: %w", err)
	}
	// The protected directory contains only per-hive stores owned by loregd.
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name() == ".lock" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return nil, err
		}
	}
	ok = true
	return &RuntimeStore{lock: lock, Path: path}, nil
}

func (s *RuntimeStore) Close() error { return s.lock.Close() }
