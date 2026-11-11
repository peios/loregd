package hivedb

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/peios/peios-go/file"
	"github.com/peios/peios-go/security"
)

const storageDirectorySDDL = "O:SYG:SYD:P(A;OICI;GA;;;SY)(A;OICI;GA;;;BA)"

type directoryOpener func(string, file.OpenOptions) (*os.File, file.Status, error)

// createDirectory supplies the descriptor to the exclusive native create. A
// directory must never be visible with inherited authority and restamped later.
func createDirectory(path string, open directoryOpener) error {
	descriptor, err := security.ParseSDDL(storageDirectorySDDL)
	if err != nil {
		return err
	}
	blob, err := descriptor.MarshalBinary()
	if err != nil {
		return err
	}
	// KACS needs a data right to select the handle's file mode. Metadata
	// rights alone return EINVAL; LIST_DIRECTORY is the directory read right.
	directory, _, err := open(path, file.OpenOptions{
		Access: file.ListDirectory | file.ReadAttributes, Disposition: file.CreateNew,
		Options: file.Directory, Flags: file.NoFollow, Descriptor: blob,
	})
	if err != nil {
		return err
	}
	return directory.Close()
}

// ensureDirectory creates missing ancestors explicitly. Existing directories
// retain their owner, descriptor and mode; symlinks and other types are errors.
func ensureDirectory(path string, open directoryOpener) error {
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err == nil {
		if !info.IsDir() {
			return &os.PathError{Op: "ensure directory", Path: path, Err: syscall.ENOTDIR}
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ensureDirectory(filepath.Dir(path), open); err != nil {
		return err
	}
	if err := createDirectory(path, open); err != nil {
		if errors.Is(err, os.ErrExist) {
			// Another creator won. Check its type without changing its policy.
			return ensureDirectory(path, open)
		}
		return fmt.Errorf("create directory %s: %w", path, err)
	}
	return nil
}

func createVolatileDirectory(root string, open directoryOpener) (string, error) {
	for range 10 {
		path := filepath.Join(root, "hive-"+rand.Text())
		if err := createDirectory(path, open); err != nil {
			if errors.Is(err, os.ErrExist) {
				continue
			}
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("create volatile directory under %s: %w", root, os.ErrExist)
}
