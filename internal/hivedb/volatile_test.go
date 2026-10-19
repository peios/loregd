package hivedb

import (
	"bufio"
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestVolatileWALAndReplacementConnection(t *testing.T) {
	h, err := Open("Machine", tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestResource(t, h)
	var mode string
	if err := h.ReadDB().QueryRow("PRAGMA volatile.journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("volatile journal: %s %v", mode, err)
	}
	conn, err := h.WriteDB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("discard connection: %v", err)
	}
	_ = conn.Close() // Raw has already discarded this intentionally invalid connection.
	// ATTACH, WAL and foreign keys are connection-local and must be restored.
	var count, foreignKeys int
	if err := h.WriteDB().QueryRow("SELECT COUNT(*) FROM volatile.keys").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := h.WriteDB().QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign keys after replacement: %d %v", foreignKeys, err)
	}
}

func TestSameHiveNameHasIndependentVolatileStore(t *testing.T) {
	a, err := Open("Machine", tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestResource(t, a)
	b, err := Open("Machine", tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestResource(t, b)
	if _, err := a.WriteDB().Exec(`INSERT INTO volatile.blanket_tombstones (key_guid,layer,sequence) VALUES (?, 'base',1)`, a.RootGUID[:]); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := b.ReadDB().QueryRow("SELECT COUNT(*) FROM volatile.blanket_tombstones").Scan(&count); err != nil || count != 0 {
		t.Fatalf("volatile store leaked between hives: %d %v", count, err)
	}
}

func runRuntimeCrashChild(t *testing.T, root string) {
	t.Helper()
	store, err := openRuntimeStore(filepath.Join(root, "run"), posixOnlyProtection)
	if err != nil {
		t.Fatal(err)
	}
	h, err := OpenIn("Machine", filepath.Join(root, "Machine.db"), store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.WriteDB().Exec(`INSERT INTO volatile.blanket_tombstones (key_guid,layer,sequence) VALUES (?, 'base',99)`, h.RootGUID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stdout.WriteString("ready\n"); err != nil {
		t.Fatal(err)
	}
	select {}
}

func startRuntimeCrashChild(t *testing.T, root string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeStoreCrashCleanup$")
	cmd.Env = append(os.Environ(), "LOREGD_CRASH_TEST_ROOT="+root)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The success path already kills and reaps the process; cover early test failures too.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	ready := make(chan string, 1)
	go func() {
		// An incomplete marker also fails the parent assertion below.
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		if line != "ready\n" {
			t.Fatal(line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not start")
	}
	return cmd
}

func TestRuntimeStoreCrashCleanup(t *testing.T) {
	if root := os.Getenv("LOREGD_CRASH_TEST_ROOT"); root != "" {
		runRuntimeCrashChild(t, root)
		return
	}
	root := t.TempDir()
	cmd := startRuntimeCrashChild(t, root)

	if s, err := openRuntimeStore(filepath.Join(root, "run"), posixOnlyProtection); err == nil {
		closeTestResource(t, s)
		t.Fatal("second daemon acquired a live store")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	var killed *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &killed) {
		t.Fatalf("expected killed child, got %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "run"))
	if err != nil || len(entries) <= 1 {
		t.Fatalf("expected crash residue: %v", err)
	}
	store, err := openRuntimeStore(filepath.Join(root, "run"), posixOnlyProtection)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestResource(t, store)
	entries, err = os.ReadDir(store.Path)
	if err != nil || len(entries) != 1 {
		t.Fatalf("crash residue retained: %v %v", entries, err)
	}
	h, err := OpenIn("Machine", filepath.Join(root, "Machine.db"), store.Path)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := h.ReadDB().QueryRow("SELECT COUNT(*) FROM volatile.blanket_tombstones").Scan(&count); err != nil || count != 0 {
		t.Fatalf("old volatile state reused: %d %v", count, err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(store.Path)
	if err != nil || len(entries) != 1 {
		t.Fatalf("clean close left residue: %v %v", entries, err)
	}
}

func TestRuntimeStoreRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	path := filepath.Join(root, "run")
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if s, err := openRuntimeStore(path, posixOnlyProtection); err == nil {
		closeTestResource(t, s)
		t.Fatal("accepted symlink")
	}
	if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
		t.Fatal("symlink target modified", err)
	}
}
