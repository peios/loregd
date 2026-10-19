---
title: Startup Sequence
description: Startup runs to completion before a single request is accepted, and any failure in it is fatal.
---

Startup runs to completion before loregd accepts a single request.
[*startup.startup-completes-before-any-request-is-accepted] Any failure
in it is fatal: loregd logs the error and exits non-zero rather than
serving a hive it could not fully prepare.
[*startup.any-startup-failure-is-fatal-and-exits-non-zero]

## 1. Parse and validate arguments [*startup.arguments-are-parsed-and-validated-first]

Extract the hive name to database path mapping and apply the validation
in §2.1.

## 2. Open each hive database [*startup.each-declared-hives-database-is-opened-or-created]

For each declared hive, create the database file's parent directory if it
is absent (mode `0700`), then open — or create — the SQLite database at
that path. [*startup.a-missing-parent-directory-is-created-mode-0700]
Registry hives can contain credentials and security policy, so
the fallback directory is private to the service. A packaged system normally
receives the same protected directory from the `dev.peios.loregd` package,
but a first boot onto an empty `/var/state` still works without anything
having prepared it.

Four pieces of connection state are established immediately:

- `PRAGMA journal_mode=wal`. The result is read back and checked; if the
  database does not report `wal`, the open fails.
  [*startup.wal-mode-is-verified-and-the-open-fails-without-it] WAL mode
  is what allows concurrent readers alongside a writer (§4.1), so
  silently running without it is not acceptable.
- `PRAGMA foreign_keys=ON`. Neither schema declares a foreign key, so
  this constrains nothing. [*startup.foreign-keys-are-enabled]
- `PRAGMA busy_timeout=0` — SQL lock conflicts return busy immediately (§4.4).
  [*startup.the-busy-timeout-is-twenty-five-seconds]
- The volatile database is attached (§3.3).

Each `database/sql` handle is limited to a single underlying connection.
[*startup.each-sql-handle-is-limited-to-one-underlying-connection]

## 3. Attach the volatile store [*startup.the-volatile-store-is-attached-as-schema-volatile]

Each hive gets a fresh tmpfs-backed SQLite database, attached as `volatile`
and put in WAL mode with private caches. Before opening hives, loregd
protects and locks `/run/loregd`, verifies tmpfs, and removes old volatile
stores left by a crash (§3.3). It never reuses a prior process's volatile data.
[*startup.volatile-stores-are-fresh-and-use-tmpfs-wal]

## 4. Establish the schema

If the database has no `schema_version` table, it is new: loregd creates
the persistent tables, creates the volatile tables, and stamps the schema
version, in one transaction.
[*startup.a-new-database-gets-its-schema-created-in-one-transaction]

If the version is present, it is compared against the version loregd
supports. A newer version aborts startup, and so does an older one
(§3.1).
[*startup.a-schema-version-mismatch-in-either-direction-aborts-startup]

## 5. First-boot root key

For each hive, look for a key with no parent. If none exists, this is the
hive's first boot: loregd generates a random 16-byte GUID for the root
key, builds the default hive-root security descriptor, and inserts the
root key record.
[*startup.a-hive-with-no-parentless-key-gets-a-generated-root-key]

The default root descriptor grants SYSTEM and Administrators full access
to the key, grants Authenticated Users read access, marks all three as
container-inheritable, and sets both owner and group to SYSTEM.
[*startup.the-default-hive-root-descriptor]

## 6. Crash recovery

SQLite's own WAL recovery handles any transaction that was uncommitted
when the process died; it happens when the database is opened and needs
nothing from loregd.
[*startup.wal-recovery-is-sqlites-own-and-needs-nothing-from-loregd]

What loregd does on top of that is clean up **orphaned keys** — key
records that no path entry in any layer points at.
[*startup.an-orphaned-key-is-one-no-path-entry-points-at] These can
survive a crash between a key's creation and the creation of the path
entry naming it. In one transaction, loregd deletes the values belonging
to orphaned keys, then their blanket tombstones, then the key records
themselves.
[*startup.orphaned-keys-are-deleted-values-then-tombstones-then-records]

The hive root is exempt: it legitimately has no parent and no path entry
pointing at it, so orphan detection skips keys whose `parent_guid` is
null. [*startup.orphan-detection-skips-keys-with-a-null-parent]

## 7. Compute the maximum sequence number

For each hive, take the maximum `sequence` across the path entries,
values, and blanket tombstones — in both the persistent and the volatile
tables — then take the maximum across all hives.
[*startup.the-maximum-sequence-is-taken-across-every-table-and-every-hive]
That single global figure is what loregd reports at registration, so the
kernel can resume allocating sequence numbers above everything already
stored.

In practice the volatile tables are empty at this point in startup and
contribute nothing.

## 8. Open the registry device [*startup.the-registry-device-is-opened-at-dev-pkm-registry]

Open `/dev/pkm_registry`. The kernel requires `SeTcbPrivilege` in the
calling thread's token to permit this; loregd performs no check of its
own and relies on the kernel to refuse.
[*startup.the-kernel-not-loregd-enforces-setcbprivilege]

Before registration, negotiate RSI lifetime extension version 1 with
`REG_SRC_NEGOTIATE`. Retain the returned source-session identifier for request
contexts and lease renewal. A kernel without this extension is incompatible;
startup fails before registering hives instead of silently changing lifetime
semantics.
[*startup.lifetime-protocol-is-negotiated-before-registration]

## 9. Register the hives

Issue `REG_SRC_REGISTER` with every hive name, its root key GUID, and the
global maximum sequence number from step 7.
[*startup.registration-sends-every-hive-name-root-guid-and-the-maximum-sequence]
The registration flags are zero: loregd registers global hives only and
never private ones.
[*startup.the-registration-flags-are-zero-so-hives-are-global]

## 10. Signal readiness and serve [*startup.readiness-then-the-signal-handler-then-the-request-loop]

Send readiness to `NOTIFY_SOCKET` if it is set (§2.1), install the
termination signal handler (§2.3), and enter the request loop (§4.2).

## Backup and repair limits

The source-checked implementation at `3cb9768` (1 October 2026) does **not**
create an automatic backup at startup. The [startup caller](https://github.com/peios/loregd/blob/3cb9768b0586ab6563f3b3c50623998624926a8d/main.go#L67-L106)
opens each hive; [hive initialization](https://github.com/peios/loregd/blob/3cb9768b0586ab6563f3b3c50623998624926a8d/internal/hivedb/hivedb.go#L50-L109)
prepares its database, schema, root, orphan cleanup and reader pool.
The [orphan cleanup](https://github.com/peios/loregd/blob/3cb9768b0586ab6563f3b3c50623998624926a8d/internal/hivedb/hivedb.go#L320-L365)
is a transaction deleting unreferenced persistent records, not backup
restoration or a database-corruption repair facility.

WAL recovery and [RSI_FLUSH checkpointing](https://github.com/peios/loregd/blob/3cb9768b0586ab6563f3b3c50623998624926a8d/internal/handler/txn_handlers.go#L117-L154)
do not create a backup copy. Separately, loregd supports
[read-only RSI snapshot transactions](https://github.com/peios/loregd/blob/3cb9768b0586ab6563f3b3c50623998624926a8d/internal/handler/txn.go#L201-L274)
used by higher-level backup operations. A transaction snapshot is not an
automatically saved startup backup. See the [LCS backup contract](~peios/lcs/backup-and-restore/backup)
for that separate operation; this startup behavior does not establish an
end-to-end offline repair or restore procedure.

Verify the existence and coverage of a recovery copy rather than assuming
startup made one. The [command-line limits](~peios/loregd/startup/command-line#unsupported-recovery-switches)
also rule out the previously described loregd repair switches for this
implementation.
