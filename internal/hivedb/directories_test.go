package hivedb

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/peios/peios-go/file"
	"github.com/peios/peios-go/security"
)

func checkedDirectoryOpener(t *testing.T, created *[]string) directoryOpener {
	t.Helper()
	return func(path string, options file.OpenOptions) (*os.File, file.Status, error) {
		t.Helper()
		// Observe policy before the host stand-in creates the directory: this
		// catches any return to mkdir followed by SetDescriptor.
		if options.Disposition != file.CreateNew || options.Options != file.Directory ||
			options.Flags != file.NoFollow || options.Access != file.ReadAttributes {
			t.Fatalf("unsafe directory creation options: %+v", options)
		}
		sd, err := security.ParseSDDL(storageDirectorySDDL)
		if err != nil {
			t.Fatal(err)
		}
		want, err := sd.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(options.Descriptor, want) {
			t.Fatalf("descriptor missing or wrong at creation: %x", options.Descriptor)
		}
		*created = append(*created, path)
		return openHostDirectory(path, options)
	}
}

func TestMissingDirectoriesReceiveDescriptorAtCreation(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "state")
	path := filepath.Join(parent, "loregd")
	var created []string
	if err := ensureDirectory(path, checkedDirectoryOpener(t, &created)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(created, []string{parent, path}) {
		t.Fatalf("creation order = %v", created)
	}
	// Existing paths must not reach the native create or descriptor APIs.
	if err := ensureDirectory(path, nil); err != nil {
		t.Fatal(err)
	}
}

func TestExistingDirectoryIsUnchanged(t *testing.T) {
	path := t.TempDir()
	if err := os.Chmod(path, 0o751); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureDirectory(path, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || before.Mode() != after.Mode() || before.ModTime() != after.ModTime() {
		t.Fatal("existing directory changed")
	}
}

func TestDirectoryPathRejectsOtherTypes(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "file")
	if err := os.WriteFile(regular, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "link")
	if err := os.Symlink(t.TempDir(), symlink); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{regular, symlink, filepath.Join(regular, "child")} {
		if err := ensureDirectory(path, nil); !errors.Is(err, syscall.ENOTDIR) || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: expected a path-specific not-a-directory error, got %v", path, err)
		}
	}
	data, err := os.ReadFile(regular)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing file modified: %q, %v", data, err)
	}
}

func TestDirectoryCreationRaceChecksWinner(t *testing.T) {
	for _, directory := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), "raced")
		open := func(path string, _ file.OpenOptions) (*os.File, file.Status, error) {
			var err error
			if directory {
				err = os.Mkdir(path, 0o751)
			} else {
				err = os.WriteFile(path, []byte("keep"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			return nil, 0, syscall.EEXIST
		}
		err := ensureDirectory(path, open)
		if directory && err != nil {
			t.Fatal(err)
		}
		if !directory && !errors.Is(err, syscall.ENOTDIR) {
			t.Fatalf("accepted non-directory race winner: %v", err)
		}
	}
}

func TestDirectoryCreationFailsWithoutPosixFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "loregd")
	open := func(string, file.OpenOptions) (*os.File, file.Status, error) {
		return nil, 0, syscall.ENOSYS
	}
	if err := ensureDirectory(path, open); !errors.Is(err, syscall.ENOSYS) {
		t.Fatalf("native failure lost: %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("unprotected fallback directory created: %v", err)
	}
}

func TestHiveAndVolatileDirectoriesAreCreatedWithPolicy(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state", "Machine.hive")
	var created []string
	hive, err := openIn("Machine", path, root, checkedDirectoryOpener(t, &created))
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestResource(t, hive)
	if len(created) != 2 || created[0] != filepath.Dir(path) || !strings.HasPrefix(created[1], filepath.Join(root, "hive-")) {
		t.Fatalf("hive storage directories = %v", created)
	}
}

func TestRuntimeDirectoryIsCreatedWithPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "loregd")
	var created []string
	store, err := openRuntimeStore(path, checkedDirectoryOpener(t, &created), false)
	if err != nil {
		t.Fatal(err)
	}
	closeTestResource(t, store)
	if len(created) != 2 || created[1] != path {
		t.Fatalf("runtime directories = %v", created)
	}
}
