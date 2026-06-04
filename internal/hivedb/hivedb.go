// Package hivedb manages per-hive SQLite database lifecycle for loregd.
//
// Each hive has one persistent database file (WAL mode) and one
// attached in-memory volatile store (shared-cache mode). The
// package handles schema creation, first-boot root key creation,
// crash recovery, max sequence computation, and read/write
// connection separation.
package hivedb

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/peios/loregd/internal/fold"
	"github.com/peios/loregd/internal/sd"

	_ "modernc.org/sqlite"
)

// ReadPoolSize returns the number of read connections per hive.
// Uses the number of available CPU cores, capped at 16, per spec.
func ReadPoolSize() int {
	n := runtime.NumCPU()
	if n > 16 {
		n = 16
	}
	if n < 1 {
		n = 1
	}
	return n
}

// HiveDB represents an open hive database with its volatile store.
type HiveDB struct {
	Name     string   // Hive name (case-preserving).
	RootGUID [16]byte // Root key GUID.
	Path     string   // Database file path.

	writeDB  *sql.DB   // Single write connection.
	readDBs  []*sql.DB // Read connection pool.
	readNext atomic.Uint32
}

// Open opens (or creates) a hive database at path. It enables WAL
// mode, attaches the volatile store, creates/migrates the schema,
// performs first-boot root creation if needed, runs crash recovery,
// and creates the read connection pool.
func Open(name, path string) (*HiveDB, error) {
	// Write connection: used for all mutations and startup.
	writeDB, err := openConn(path, name)
	if err != nil {
		return nil, fmt.Errorf("open write connection for %s: %w", name, err)
	}

	if err := ensureSchema(writeDB); err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("schema setup for %s: %w", name, err)
	}

	rootGUID, err := ensureRootKey(writeDB, name)
	if err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("root key setup for %s: %w", name, err)
	}

	if err := cleanOrphans(writeDB); err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("crash recovery for %s: %w", name, err)
	}

	// Read pool: multiple connections for concurrent reads.
	poolSize := ReadPoolSize()
	readDBs := make([]*sql.DB, 0, poolSize)
	for range poolSize {
		rdb, err := openConn(path, name)
		if err != nil {
			// Close already-opened connections.
			for _, r := range readDBs {
				r.Close()
			}
			writeDB.Close()
			return nil, fmt.Errorf("open read connection for %s: %w", name, err)
		}
		readDBs = append(readDBs, rdb)
	}

	return &HiveDB{
		Name:     name,
		RootGUID: rootGUID,
		Path:     path,
		writeDB:  writeDB,
		readDBs:  readDBs,
	}, nil
}

// openConn opens a single SQLite connection with WAL, foreign keys,
// and volatile store attached.
func openConn(path, hiveName string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	if err := enableWAL(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("WAL: %w", err)
	}
	if err := enableForeignKeys(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("foreign keys: %w", err)
	}
	if err := setBusyTimeout(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("busy timeout: %w", err)
	}
	if err := attachVolatile(db, hiveName); err != nil {
		db.Close()
		return nil, fmt.Errorf("volatile attach: %w", err)
	}
	return db, nil
}

// WriteDB returns the single write connection.
func (h *HiveDB) WriteDB() *sql.DB { return h.writeDB }

// ReadDB returns a read connection from the pool (round-robin).
func (h *HiveDB) ReadDB() *sql.DB {
	idx := h.readNext.Add(1) - 1
	return h.readDBs[idx%uint32(len(h.readDBs))]
}

// OpenSnapshotConn opens a dedicated connection to the hive database for a
// read-only point-in-time snapshot transaction (RSI_TXN_READ_ONLY, used by
// REG_IOC_BACKUP). It is separate from the read pool so a long-lived
// snapshot does not starve concurrent readers, and from the write
// connection so it never contends for the write lock. WAL mode gives the
// snapshot stable MVCC isolation for persistent data once its first read
// runs. The caller MUST Close it when the snapshot transaction is released.
func (h *HiveDB) OpenSnapshotConn() (*sql.DB, error) {
	return openConn(h.Path, h.Name)
}

// MaxSequence returns the highest sequence number across all tables
// in this hive (persistent and volatile).
func (h *HiveDB) MaxSequence() (uint64, error) {
	var maxSeq sql.NullInt64
	err := h.ReadDB().QueryRow(`
		SELECT MAX(seq) FROM (
			SELECT MAX(sequence) AS seq FROM main.path_entries
			UNION ALL
			SELECT MAX(sequence) FROM main.[values]
			UNION ALL
			SELECT MAX(sequence) FROM main.blanket_tombstones
			UNION ALL
			SELECT MAX(sequence) FROM volatile.path_entries
			UNION ALL
			SELECT MAX(sequence) FROM volatile.[values]
			UNION ALL
			SELECT MAX(sequence) FROM volatile.blanket_tombstones
		)
	`).Scan(&maxSeq)
	if err != nil {
		return 0, fmt.Errorf("max sequence: %w", err)
	}
	if !maxSeq.Valid {
		return 0, nil
	}
	return uint64(maxSeq.Int64), nil
}

// Close closes all database connections.
func (h *HiveDB) Close() error {
	var firstErr error
	for _, r := range h.readDBs {
		if err := r.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := h.writeDB.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// DB returns the write connection for backward compatibility with
// Slice 1 tests.
func (h *HiveDB) DB() *sql.DB { return h.writeDB }

func enableWAL(db *sql.DB) error {
	var mode string
	err := db.QueryRow("PRAGMA journal_mode=wal").Scan(&mode)
	if err != nil {
		return err
	}
	if mode != "wal" {
		return fmt.Errorf("expected WAL mode, got %q", mode)
	}
	return nil
}

func enableForeignKeys(db *sql.DB) error {
	_, err := db.Exec("PRAGMA foreign_keys=ON")
	return err
}

// BusyTimeoutMs is the compiled-in busy timeout for SQLite connections.
// Set shorter than LCS's default RequestTimeoutMs (30s) so loregd
// responds with RSI_TXN_BUSY before LCS times out the caller.
const BusyTimeoutMs = 25000

func setBusyTimeout(db *sql.DB) error {
	_, err := db.Exec("PRAGMA busy_timeout=25000")
	return err
}

func attachVolatile(db *sql.DB, hiveName string) error {
	uri := fmt.Sprintf("file:%s_volatile?mode=memory&cache=shared", hiveName)
	_, err := db.Exec("ATTACH ? AS volatile", uri)
	return err
}

func ensureSchema(db *sql.DB) error {
	var tableExists int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_version'",
	).Scan(&tableExists)
	if err != nil {
		return err
	}

	if tableExists == 0 {
		// First boot: create schema and version in a single transaction
		// so a crash cannot leave the database half-initialized.
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin schema tx: %w", err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(schemaDDL); err != nil {
			return fmt.Errorf("create schema: %w", err)
		}
		if _, err := tx.Exec("INSERT INTO schema_version (version) VALUES (?)", schemaVersion); err != nil {
			return fmt.Errorf("insert schema version: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit schema: %w", err)
		}
	} else {
		var version int
		if err := db.QueryRow("SELECT version FROM schema_version").Scan(&version); err != nil {
			return fmt.Errorf("read schema version: %w", err)
		}
		if version > schemaVersion {
			return fmt.Errorf("database schema version %d is newer than loregd supports (%d)", version, schemaVersion)
		}
		if version < schemaVersion {
			return fmt.Errorf("database schema version %d requires migration (loregd supports %d, no migrations implemented)", version, schemaVersion)
		}
	}

	if _, err := db.Exec(volatileSchemaDDL); err != nil {
		return fmt.Errorf("create volatile schema: %w", err)
	}

	return nil
}

func ensureRootKey(db *sql.DB, hiveName string) ([16]byte, error) {
	var guid [16]byte
	var guidSlice []byte
	err := db.QueryRow("SELECT guid FROM keys WHERE parent_guid IS NULL").Scan(&guidSlice)
	if err == nil {
		if len(guidSlice) != 16 {
			return guid, fmt.Errorf("root key GUID has invalid length %d", len(guidSlice))
		}
		copy(guid[:], guidSlice)
		return guid, nil
	}
	if err != sql.ErrNoRows {
		return guid, fmt.Errorf("check root key: %w", err)
	}

	if _, err := rand.Read(guid[:]); err != nil {
		return guid, fmt.Errorf("generate root GUID: %w", err)
	}

	defaultSD := sd.DefaultHiveRootSD()
	now := time.Now().UnixNano()

	_, err = db.Exec(`
		INSERT INTO keys (guid, name, name_folded, parent_guid, sd, volatile, symlink, last_write_time)
		VALUES (?, ?, ?, NULL, ?, 0, 0, ?)
	`, guid[:], hiveName, fold.String(hiveName), defaultSD, now)
	if err != nil {
		return guid, fmt.Errorf("create root key: %w", err)
	}

	return guid, nil
}

func cleanOrphans(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("clean orphans begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		DELETE FROM main.[values] WHERE key_guid IN (
			SELECT k.guid FROM main.keys k
			WHERE k.parent_guid IS NOT NULL
			AND k.guid NOT IN (
				SELECT target_guid FROM main.path_entries WHERE target_type = 0
			)
		)
	`); err != nil {
		return fmt.Errorf("clean orphan values: %w", err)
	}

	if _, err := tx.Exec(`
		DELETE FROM main.blanket_tombstones WHERE key_guid IN (
			SELECT k.guid FROM main.keys k
			WHERE k.parent_guid IS NOT NULL
			AND k.guid NOT IN (
				SELECT target_guid FROM main.path_entries WHERE target_type = 0
			)
		)
	`); err != nil {
		return fmt.Errorf("clean orphan tombstones: %w", err)
	}

	if _, err := tx.Exec(`
		DELETE FROM main.keys
		WHERE parent_guid IS NOT NULL
		AND guid NOT IN (
			SELECT target_guid FROM main.path_entries WHERE target_type = 0
		)
	`); err != nil {
		return fmt.Errorf("clean orphan keys: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("clean orphans commit: %w", err)
	}
	return nil
}
