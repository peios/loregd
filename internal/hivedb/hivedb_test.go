package hivedb

import (
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func tempDBPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "test.regdb")
}

func TestOpenCreatesNewDatabase(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("TestHive", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer h.Close()

	// Database file should exist.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file not created: %v", err)
	}

	// Name should be preserved.
	if h.Name != "TestHive" {
		t.Errorf("Name = %q, want %q", h.Name, "TestHive")
	}

	// Root GUID should be 16 bytes, not all zeros.
	allZero := true
	for _, b := range h.RootGUID {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("RootGUID is all zeros")
	}
}

func TestOpenExistingDatabase(t *testing.T) {
	path := tempDBPath(t)

	// First open: creates the database.
	h1, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	guid1 := h1.RootGUID
	h1.Close()

	// Second open: should find existing root key.
	h2, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer h2.Close()

	if h2.RootGUID != guid1 {
		t.Errorf("root GUID changed across restarts: %x vs %x", guid1, h2.RootGUID)
	}
}

func TestWALMode(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer h.Close()

	var mode string
	if err := h.DB().QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}

func TestBusyTimeout(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer h.Close()

	var timeout int
	if err := h.DB().QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if timeout != BusyTimeoutMs {
		t.Errorf("busy_timeout = %d, want %d", timeout, BusyTimeoutMs)
	}
}

func TestSchemaVersion(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer h.Close()

	var version int
	if err := h.DB().QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("schema version = %d, want %d", version, schemaVersion)
	}
}

func TestSchemaVersionTooNew(t *testing.T) {
	path := tempDBPath(t)

	// Create a database with a future schema version.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("CREATE TABLE schema_version (version INTEGER NOT NULL)")
	db.Exec("INSERT INTO schema_version (version) VALUES (?)", schemaVersion+1)
	db.Close()

	_, err = Open("Machine", path)
	if err == nil {
		t.Fatal("expected error for too-new schema version")
	}
}

func TestTablesExist(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer h.Close()

	tables := []string{"schema_version", "keys", "path_entries", "values", "blanket_tombstones"}
	for _, table := range tables {
		var count int
		err := h.DB().QueryRow(
			"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&count)
		if err != nil {
			t.Errorf("query for table %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s: count = %d, want 1", table, count)
		}
	}
}

func TestVolatileTablesExist(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer h.Close()

	// Volatile tables are in the 'volatile' attached database.
	// "values" is a SQLite keyword and must be bracket-escaped.
	queries := map[string]string{
		"keys":               "SELECT COUNT(*) FROM volatile.keys",
		"path_entries":       "SELECT COUNT(*) FROM volatile.path_entries",
		"values":             "SELECT COUNT(*) FROM volatile.[values]",
		"blanket_tombstones": "SELECT COUNT(*) FROM volatile.blanket_tombstones",
	}
	for table, query := range queries {
		_, err := h.DB().Exec(query)
		if err != nil {
			t.Errorf("volatile table %s does not exist: %v", table, err)
		}
	}
}

func TestRootKeySD(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer h.Close()

	var sdBytes []byte
	err = h.DB().QueryRow("SELECT sd FROM keys WHERE guid = ?", h.RootGUID[:]).Scan(&sdBytes)
	if err != nil {
		t.Fatalf("query root SD: %v", err)
	}

	// Verify it's a valid self-relative SD.
	if len(sdBytes) < 20 {
		t.Fatalf("SD too short: %d bytes", len(sdBytes))
	}
	if sdBytes[0] != 1 { // Revision
		t.Errorf("SD revision = %d, want 1", sdBytes[0])
	}
	control := binary.LittleEndian.Uint16(sdBytes[2:4])
	if control&0x8000 == 0 {
		t.Error("SD missing SE_SELF_RELATIVE flag")
	}
	if control&0x0004 == 0 {
		t.Error("SD missing SE_DACL_PRESENT flag")
	}
}

func TestRootKeyProperties(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer h.Close()

	var name string
	var nameFolded string
	var parentGUID []byte
	var volatile, symlink int

	err = h.DB().QueryRow(`
		SELECT name, name_folded, parent_guid, volatile, symlink
		FROM keys WHERE guid = ?
	`, h.RootGUID[:]).Scan(&name, &nameFolded, &parentGUID, &volatile, &symlink)
	if err != nil {
		t.Fatalf("query root key: %v", err)
	}

	if name != "Machine" {
		t.Errorf("name = %q, want %q", name, "Machine")
	}
	if nameFolded != "machine" {
		t.Errorf("name_folded = %q, want %q", nameFolded, "machine")
	}
	if parentGUID != nil {
		t.Errorf("parent_guid = %x, want nil", parentGUID)
	}
	if volatile != 0 {
		t.Errorf("volatile = %d, want 0", volatile)
	}
	if symlink != 0 {
		t.Errorf("symlink = %d, want 0", symlink)
	}
}

func TestCrashRecoveryOrphanedGUID(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Insert an orphaned key (has no path entries).
	orphanGUID := [16]byte{0xDE, 0xAD, 0xBE, 0xEF}
	_, err = h.DB().Exec(`
		INSERT INTO keys (guid, name, name_folded, parent_guid, sd, volatile, symlink, last_write_time)
		VALUES (?, 'orphan', 'orphan', ?, X'00', 0, 0, 0)
	`, orphanGUID[:], h.RootGUID[:])
	if err != nil {
		t.Fatalf("insert orphan: %v", err)
	}

	// Insert a value for the orphan.
	_, err = h.DB().Exec(`
		INSERT INTO [values] (key_guid, name, name_folded, layer, type, data, sequence)
		VALUES (?, 'test', 'test', 'base', 1, X'00', 1)
	`, orphanGUID[:])
	if err != nil {
		t.Fatalf("insert orphan value: %v", err)
	}

	h.Close()

	// Reopen — crash recovery should clean the orphan.
	h2, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer h2.Close()

	var count int
	h2.DB().QueryRow("SELECT COUNT(*) FROM keys WHERE guid = ?", orphanGUID[:]).Scan(&count)
	if count != 0 {
		t.Error("orphaned key was not cleaned up")
	}

	h2.DB().QueryRow("SELECT COUNT(*) FROM [values] WHERE key_guid = ?", orphanGUID[:]).Scan(&count)
	if count != 0 {
		t.Error("orphaned key's values were not cleaned up")
	}
}

func TestCrashRecoveryPreservesNonOrphans(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Insert a non-orphaned key (has a path entry).
	childGUID := [16]byte{0x01, 0x02, 0x03, 0x04}
	_, err = h.DB().Exec(`
		INSERT INTO keys (guid, name, name_folded, parent_guid, sd, volatile, symlink, last_write_time)
		VALUES (?, 'child', 'child', ?, X'00', 0, 0, 0)
	`, childGUID[:], h.RootGUID[:])
	if err != nil {
		t.Fatalf("insert child: %v", err)
	}
	_, err = h.DB().Exec(`
		INSERT INTO path_entries (parent_guid, child_name, child_name_folded, layer, target_type, target_guid, sequence)
		VALUES (?, 'child', 'child', 'base', 0, ?, 1)
	`, h.RootGUID[:], childGUID[:])
	if err != nil {
		t.Fatalf("insert path entry: %v", err)
	}

	h.Close()

	// Reopen — non-orphan should survive.
	h2, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer h2.Close()

	var count int
	h2.DB().QueryRow("SELECT COUNT(*) FROM keys WHERE guid = ?", childGUID[:]).Scan(&count)
	if count != 1 {
		t.Error("non-orphaned key was incorrectly removed")
	}
}

func TestMaxSequenceEmpty(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer h.Close()

	seq, err := h.MaxSequence()
	if err != nil {
		t.Fatalf("MaxSequence: %v", err)
	}
	if seq != 0 {
		t.Errorf("MaxSequence = %d, want 0 for empty database", seq)
	}
}

func TestMaxSequenceWithData(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer h.Close()

	// Insert entries with known sequences.
	guid := h.RootGUID[:]
	h.DB().Exec(`
		INSERT INTO path_entries (parent_guid, child_name, child_name_folded, layer, target_type, target_guid, sequence)
		VALUES (?, 'a', 'a', 'base', 0, ?, 42)
	`, guid, guid)
	h.DB().Exec(`
		INSERT INTO [values] (key_guid, name, name_folded, layer, type, data, sequence)
		VALUES (?, 'v', 'v', 'base', 1, X'00', 100)
	`, guid)
	h.DB().Exec(`
		INSERT INTO blanket_tombstones (key_guid, layer, sequence)
		VALUES (?, 'layer1', 7)
	`, guid)

	seq, err := h.MaxSequence()
	if err != nil {
		t.Fatalf("MaxSequence: %v", err)
	}
	if seq != 100 {
		t.Errorf("MaxSequence = %d, want 100", seq)
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer h.Close()

	var fk int
	if err := h.DB().QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}

	// Also verify on a read connection.
	var fkRead int
	if err := h.ReadDB().QueryRow("PRAGMA foreign_keys").Scan(&fkRead); err != nil {
		t.Fatalf("PRAGMA foreign_keys (read): %v", err)
	}
	if fkRead != 1 {
		t.Errorf("foreign_keys (read) = %d, want 1", fkRead)
	}
}

func TestSchemaVersionSingleRow(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer h.Close()

	var count int
	if err := h.DB().QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&count); err != nil {
		t.Fatalf("count schema_version: %v", err)
	}
	if count != 1 {
		t.Errorf("schema_version row count = %d, want 1", count)
	}
}

func TestRootKeyGUIDIsRandom(t *testing.T) {
	// Two different databases should get different root GUIDs.
	h1, err := Open("A", tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer h1.Close()

	h2, err := Open("B", tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()

	if h1.RootGUID == h2.RootGUID {
		t.Error("two databases got the same root GUID")
	}
}
