---
title: Persistent Tables
description: The four data tables every hive database has — keys, path entries, values and blanket tombstones — and their columns.
---

Each hive is one SQLite database, and every hive database has the same
four data tables.
[*tables.every-hive-database-has-the-same-four-data-tables] loregd creates
them on first boot (§2.2, step 4).

The tables hold what the kernel gives loregd and nothing derived from it.
Security descriptors are stored as opaque blobs, GUIDs and sequence
numbers are assigned by the kernel, and no table records a resolved or
filtered view of anything.
[*tables.no-table-records-a-derived-view-of-kernel-data]

## keys [*tables.the-keys-table]

```sql
CREATE TABLE keys (
    guid           BLOB NOT NULL PRIMARY KEY,
    name           TEXT NOT NULL,
    name_folded    TEXT NOT NULL,
    parent_guid    BLOB,
    sd             BLOB NOT NULL,
    volatile       INTEGER NOT NULL DEFAULT 0,
    symlink        INTEGER NOT NULL DEFAULT 0,
    last_write_time INTEGER NOT NULL
);
```

| Column | Meaning |
|---|---|
| `guid` | The 16-byte key GUID assigned by the kernel. Primary key. |
| `name` | The key's own name component, with case preserved as written. [*tables.a-key-name-is-stored-with-its-case-preserved] |
| `name_folded` | The folded form of `name` (§3.4), used for case-insensitive lookup. |
| `parent_guid` | The parent key's GUID; null for the hive root, which is how the root is identified. [*tables.the-hive-root-is-the-key-with-a-null-parent] |
| `sd` | The security descriptor, in binary self-relative form. Opaque to loregd. |
| `volatile` | 1 for a volatile key, 0 for a persistent one. In this table it is always 0 [*tables.the-persistent-keys-table-always-stores-volatile-zero] — volatile keys live in the volatile database (§3.3). |
| `symlink` | 1 if the key is a symbolic link. |
| `last_write_time` | Unix nanoseconds. |

## path_entries [*tables.the-path-entries-table]

```sql
CREATE TABLE path_entries (
    parent_guid       BLOB NOT NULL,
    child_name        TEXT NOT NULL,
    child_name_folded TEXT NOT NULL,
    layer             TEXT NOT NULL,
    target_type       INTEGER NOT NULL,
    target_guid       BLOB,
    sequence          INTEGER NOT NULL,
    PRIMARY KEY (parent_guid, child_name_folded, layer)
);

CREATE INDEX idx_path_entries_target
    ON path_entries (target_guid)
    WHERE target_type = 0;
```

A path entry is one layer's opinion about one child name under one
parent. [*tables.a-path-entry-is-one-layers-opinion-about-one-child-name]
Several layers may hold entries for the same name; resolving between them
is the kernel's job, not loregd's.
[*tables.loregd-does-not-resolve-between-layers]

| Column | Meaning |
|---|---|
| `parent_guid` | The parent key's GUID. |
| `child_name` | The child name with case preserved. |
| `child_name_folded` | The folded form, which is what the primary key uses — so a name collides case-insensitively within a layer. [*tables.a-child-name-collides-case-insensitively-within-a-layer] |
| `layer` | The layer name. Compared as binary, so layer names *are* case-sensitive, unlike key names. [*tables.layer-names-are-compared-as-binary] |
| `target_type` | 0 for a GUID entry (the key exists in this layer), 1 for HIDDEN (a tombstone masking lower layers). [*tables.target-type-one-is-a-hidden-tombstone] |
| `target_guid` | The target key's GUID when `target_type` is 0; null for HIDDEN. |
| `sequence` | The kernel-assigned sequence number. |

The partial index on `target_guid` covers only non-HIDDEN rows.
[*tables.the-target-guid-index-covers-only-non-hidden-rows] It is what
makes the reverse lookup — which path entries point at this key — cheap,
and that reverse lookup is what orphan detection (§2.2, step 6) and
`RSI_DROP_KEY` need.

## values [*tables.the-values-table]

```sql
CREATE TABLE [values] (
    key_guid       BLOB NOT NULL,
    name           TEXT NOT NULL,
    name_folded    TEXT NOT NULL,
    layer          TEXT NOT NULL,
    type           INTEGER NOT NULL,
    data           BLOB,
    sequence       INTEGER NOT NULL,
    PRIMARY KEY (key_guid, name_folded, layer)
);
```

| Column | Meaning |
|---|---|
| `key_guid` | The key this value belongs to. |
| `name` | The value name, case preserved. The empty string is the key's default value. [*tables.the-empty-value-name-is-the-keys-default-value] |
| `name_folded` | The folded form; also the empty string for the default value. |
| `layer` | The layer this value entry belongs to. |
| `type` | The registry value type — `REG_SZ` is 1, `REG_DWORD` is 4, and so on. `REG_TOMBSTONE` (`0xFFFF`) marks a per-value tombstone. [*tables.reg-tombstone-marks-a-per-value-tombstone] |
| `data` | The value payload; null for a tombstone. |
| `sequence` | The kernel-assigned sequence number. |

`values` is a reserved word in SQL, so every reference to this table is
quoted — `[values]`, or `main.[values]` and `volatile.[values]` when the
schema is named explicitly. Unquoted, it is a syntax error.

## blanket_tombstones [*tables.the-blanket-tombstones-table]

```sql
CREATE TABLE blanket_tombstones (
    key_guid       BLOB NOT NULL,
    layer          TEXT NOT NULL,
    sequence       INTEGER NOT NULL,
    PRIMARY KEY (key_guid, layer)
);
```

A blanket tombstone hides *every* value a key holds in the layers beneath
it, rather than naming one value the way a `REG_TOMBSTONE` entry does.
One row per key per layer.
