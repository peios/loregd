package hivedb

// schemaVersion is the current loregd schema version.
const schemaVersion = 1

// Schema DDL for a hive database (persistent tables in main).
const schemaDDL = `
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS keys (
    guid           BLOB NOT NULL PRIMARY KEY,
    name           TEXT NOT NULL,
    name_folded    TEXT NOT NULL,
    parent_guid    BLOB,
    sd             BLOB NOT NULL,
    volatile       INTEGER NOT NULL DEFAULT 0,
    symlink        INTEGER NOT NULL DEFAULT 0,
    last_write_time INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS path_entries (
    parent_guid       BLOB NOT NULL,
    child_name        TEXT NOT NULL,
    child_name_folded TEXT NOT NULL,
    layer             TEXT NOT NULL,
    target_type       INTEGER NOT NULL,
    target_guid       BLOB,
    sequence          INTEGER NOT NULL,
    PRIMARY KEY (parent_guid, child_name_folded, layer)
);

CREATE INDEX IF NOT EXISTS idx_path_entries_target
    ON path_entries (target_guid)
    WHERE target_type = 0;

CREATE TABLE IF NOT EXISTS [values] (
    key_guid       BLOB NOT NULL,
    name           TEXT NOT NULL,
    name_folded    TEXT NOT NULL,
    layer          TEXT NOT NULL,
    type           INTEGER NOT NULL,
    data           BLOB,
    sequence       INTEGER NOT NULL,
    PRIMARY KEY (key_guid, name_folded, layer)
);

CREATE TABLE IF NOT EXISTS blanket_tombstones (
    key_guid       BLOB NOT NULL,
    layer          TEXT NOT NULL,
    sequence       INTEGER NOT NULL,
    PRIMARY KEY (key_guid, layer)
);
`

// Volatile schema DDL (in-memory attached database).
const volatileSchemaDDL = `
CREATE TABLE IF NOT EXISTS volatile.keys (
    guid           BLOB NOT NULL PRIMARY KEY,
    name           TEXT NOT NULL,
    name_folded    TEXT NOT NULL,
    parent_guid    BLOB,
    sd             BLOB NOT NULL,
    volatile       INTEGER NOT NULL DEFAULT 1,
    symlink        INTEGER NOT NULL DEFAULT 0,
    last_write_time INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS volatile.path_entries (
    parent_guid       BLOB NOT NULL,
    child_name        TEXT NOT NULL,
    child_name_folded TEXT NOT NULL,
    layer             TEXT NOT NULL,
    target_type       INTEGER NOT NULL,
    target_guid       BLOB,
    sequence          INTEGER NOT NULL,
    PRIMARY KEY (parent_guid, child_name_folded, layer)
);

CREATE INDEX IF NOT EXISTS volatile.idx_path_entries_target
    ON path_entries (target_guid)
    WHERE target_type = 0;

CREATE TABLE IF NOT EXISTS volatile.[values] (
    key_guid       BLOB NOT NULL,
    name           TEXT NOT NULL,
    name_folded    TEXT NOT NULL,
    layer          TEXT NOT NULL,
    type           INTEGER NOT NULL,
    data           BLOB,
    sequence       INTEGER NOT NULL,
    PRIMARY KEY (key_guid, name_folded, layer)
);

CREATE TABLE IF NOT EXISTS volatile.blanket_tombstones (
    key_guid       BLOB NOT NULL,
    layer          TEXT NOT NULL,
    sequence       INTEGER NOT NULL,
    PRIMARY KEY (key_guid, layer)
);
`
