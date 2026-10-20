---
title: The Volatile Store
description: Volatile keys never reach the hive file, so each hive attaches a second tmpfs-backed WAL database mirroring the persistent schema.
---

Volatile keys never reach the persistent hive file.
[*volatile.volatile-keys-never-reach-the-hive-file]
 Each hive attaches a
separate SQLite database as `volatile`, using WAL and private connection
caches. Its files live on tmpfs beneath `/run/loregd/hive-<random>/`.
[*volatile.each-hive-gets-a-private-tmpfs-wal-database]

The runtime directory has a protected, inheritable SYSTEM-only KACS DACL.
Startup verifies that it is on tmpfs and refuses symlinks. A stable `.lock`
file prevents two daemon instances from cleaning up each other's stores.
[*volatile.runtime-storage-is-protected-and-exclusively-owned]

## A mirror of the persistent schema
[*volatile.the-schema-mirrors-the-persistent-one]

The volatile database carries the same four tables as the persistent one
`keys`, `path_entries`, `[values]`, and `blanket_tombstones`; with
identical columns, identical primary keys, and the same partial
`idx_path_entries_target` index on non-HIDDEN path entries. Column
meanings are exactly those in §3.2.

There is one deliberate difference. In `volatile.keys` the `volatile`
column defaults to **1** rather than 0, and loregd writes 1 into it.
[*volatile.the-volatile-column-defaults-to-one]
 Every record in this
database is volatile by definition, and the column carries that fact back
out in responses without a second lookup.

Because the two schemas are structurally identical, a query that needs
both stores is a `UNION ALL` across them rather than two queries merged
in application code:

```sql
SELECT 1 FROM main.keys WHERE guid = ?
UNION ALL
SELECT 1 FROM volatile.keys WHERE guid = ?
LIMIT 1
```

## Why this shape matters

A SQLite transaction spans the attached schemas. A per-hive publication
gate prevents concurrent readers from observing a commit halfway through
publishing the two stores. Rollback discards changes to both stores.
SQLite WAL does not provide crash-atomic commits across attached files.
After a crash, persistent WAL recovery and volatile-store disposal have
different lifetimes (§4.3).
[*volatile.a-transaction-across-both-stores-commits-or-rolls-back-as-one-unit]

Volatile writes inside a transaction are invisible to other readers until
commit and disappear on rollback for the same reason persistent ones do
see §4.3.
[*volatile.volatile-writes-are-invisible-until-commit-and-vanish-on-rollback]

## Lifetime

Every open hive gets a fresh random directory. Read, write, and snapshot
connections attach the same database file for that hive. Different hive
instances never share a store merely because their names match.
[*volatile.connections-share-only-their-hives-store]

Clean shutdown closes the connections and removes the hive directories.
After a crash, files can remain in tmpfs until the next start or reboot.
The next daemon acquires the runtime lock and removes that residue before
creating fresh stores; it never reads or recovers old volatile data.
[*volatile.crash-residue-is-removed-before-fresh-stores-are-created]
The `.lock` file remains; its kernel lock ends when the process exits.
Every volatile table is empty on startup, including a restart within the
same boot.
[*volatile.the-volatile-tables-are-always-empty-at-startup]

## Which store an operation uses

For an operation naming a single key, the key's own storage decides:
loregd looks the GUID up in `main.keys` and `volatile.keys`, and whichever
holds it determines where the reads and writes go.
[*volatile.a-single-key-operation-follows-the-keys-own-store]

`RSI_CREATE_KEY` is the exception, because the key does not exist yet
the volatile flag in the request decides which database receives it.
[*volatile.create-key-is-routed-by-the-requests-volatile-flag]

Two operations are not scoped to one store at all. `RSI_LOOKUP` and
`RSI_ENUM_CHILDREN` consult both and combine the results, because a
persistent parent can legitimately have volatile children.
[*volatile.lookup-and-enum-children-consult-both-stores]
 The reverse
does not arise: a persistent child beneath a volatile parent is forbidden
by the kernel's data model, so a volatile key's whole subtree is
volatile.
[*volatile.a-volatile-keys-whole-subtree-is-volatile]
