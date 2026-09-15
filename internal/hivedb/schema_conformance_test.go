package hivedb

// Conformance tests for schema-version handling and connection limiting that
// the loregd TRM (learn loregd book, chapters 2.2 and 3.1) states but the
// integration suite cannot reach from a guest: the image ships no sqlite3 to
// construct these database states, and the connection limit is not observable
// through the registry. Cited from tests/loregd/{schema-version,
// startup-sequence}.test.lua via covered_by. PEI-1121.

import (
	"database/sql"
	"strings"
	"testing"
)

// A database whose version is below the one loregd supports fails startup, and
// the failure reports that migration is required — loregd carries no migration
// machinery. (schema.a-version-below-one-fails-startup,
// schema.there-are-no-migrations)
func TestSchemaVersionBelowOneRequiresMigration(t *testing.T) {
	path := tempDBPath(t)

	// Open creates a valid version-1 hive with every data table; closing it
	// leaves a well-formed database to perturb.
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("initial Open: %v", err)
	}
	h.Close()

	// Drop the version below 1. Everything else about the database is intact,
	// so the version check is the only thing that can fail the reopen.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE schema_version SET version = 0"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	_, err = Open("Machine", path)
	if err == nil {
		t.Fatal("expected startup to fail for a schema version below 1")
	}
	if !strings.Contains(err.Error(), "migration") {
		t.Errorf("error should report that migration is required (no migrations "+
			"are implemented); got: %v", err)
	}
}

// A second row in the single-row schema_version table is not detected: the
// first row read wins, so a database whose first row is the supported version
// starts normally even though a later row says otherwise.
// (schema.a-second-version-row-is-not-detected)
func TestSchemaVersionSecondRowNotDetected(t *testing.T) {
	path := tempDBPath(t)

	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("initial Open: %v", err)
	}
	root := h.RootGUID
	h.Close()

	// Add a second row claiming a future version. The first row (the supported
	// version, inserted at creation) still sits ahead of it by rowid.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO schema_version (version) VALUES (?)", schemaVersion+1); err != nil {
		t.Fatal(err)
	}
	db.Close()

	h2, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("a second version row should be ignored, not fail startup: %v", err)
	}
	defer h2.Close()
	if h2.RootGUID != root {
		t.Errorf("root GUID changed across the reopen: %x vs %x", root, h2.RootGUID)
	}
}

// A database holding the data tables but no schema_version table is treated as
// new: loregd stamps version 1 and proceeds, without validating that the
// existing contents match that layout.
// (schema.a-database-without-a-version-table-is-stamped-unvalidated)
func TestSchemaVersionMissingTableStampedUnvalidated(t *testing.T) {
	path := tempDBPath(t)

	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("initial Open: %v", err)
	}
	h.Close()

	// Remove the version table and plant a table that a genuine version-1
	// database would never carry. If the reopen validated the layout it would
	// have to reject or reconcile this; instead it just stamps version 1.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE schema_version"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE stale_marker (x INTEGER)"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	h2, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("a database without a version table should be stamped, not fail: %v", err)
	}
	defer h2.Close()

	var version int
	if err := h2.DB().QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("schema_version was not stamped: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("stamped version = %d, want %d", version, schemaVersion)
	}

	// The non-conforming table survived untouched — nothing validated or
	// migrated the contents, which is the unvalidated stamp the spec describes.
	var count int
	if err := h2.DB().QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='stale_marker'",
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("the pre-existing non-conforming table was disturbed (count=%d); "+
			"the stamp should be unvalidated", count)
	}
}

// Every database/sql handle a hive holds — the write connection, each read
// connection, and a snapshot connection — is limited to a single underlying
// connection. That single limit is what serialises writes (§4.1).
// (startup.each-sql-handle-is-limited-to-one-underlying-connection)
func TestEachSQLHandleHasOneConnection(t *testing.T) {
	path := tempDBPath(t)
	h, err := Open("Machine", path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer h.Close()

	if got := h.WriteDB().Stats().MaxOpenConnections; got != 1 {
		t.Errorf("write handle MaxOpenConnections = %d, want 1", got)
	}
	// ReadDB rotates round-robin; a full lap covers every pool handle.
	for i := 0; i < ReadPoolSize(); i++ {
		if got := h.ReadDB().Stats().MaxOpenConnections; got != 1 {
			t.Errorf("read handle MaxOpenConnections = %d, want 1", got)
		}
	}
	snap, err := h.OpenSnapshotConn()
	if err != nil {
		t.Fatalf("OpenSnapshotConn: %v", err)
	}
	defer snap.Close()
	if got := snap.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("snapshot handle MaxOpenConnections = %d, want 1", got)
	}
}
