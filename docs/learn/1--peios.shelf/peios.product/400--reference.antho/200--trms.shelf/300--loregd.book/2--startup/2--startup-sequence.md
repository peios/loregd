---
title: Startup Sequence
description: Preparing databases, registering hives, and notifying the service manager before dispatch begins.
---

Startup runs to completion before loregd accepts a single request.
[*startup.startup-completes-before-any-request-is-accepted]
 Database preparation, device negotiation, and registration failures are
fatal: loregd logs the error and exits with status 1. Readiness notification
is best effort; a failure is logged and serving continues.
[*startup.any-startup-failure-is-fatal-and-exits-non-zero]

## 1. parse and validate arguments
[*startup.arguments-are-parsed-and-validated-first]

loregd extracts the hive-to-database mapping and validates it as described
in §2.1. Before opening a hive, it creates or opens `/run/loregd`, verifies
tmpfs, applies its SYSTEM-only descriptor, and takes the runtime lock.
It removes old volatile stores only after taking that lock.

## 2. open each hive database
[*startup.each-declared-hives-database-is-opened-or-created]

For each declared hive, create the database file's parent directory if it
is absent (mode `0700`), then open; or create; the SQLite database at
that path.
[*startup.a-missing-parent-directory-is-created-mode-0700]
Registry hives can contain credentials and security policy, so
the fallback directory is private to the service. A packaged system normally
receives the same protected directory from the `dev.peios.loregd` package,
but a first boot onto an empty `/var/state` still works without anything
having prepared it.

Four pieces of connection state are established immediately:

- `PRAGMA journal_mode=wal`. The result is read back and checked; if the
  database does not report `wal`, the open fails.
[*startup.wal-mode-is-verified-and-the-open-fails-without-it]
 WAL mode
  is what allows concurrent readers alongside a writer (§4.1), so
  silently running without it is not acceptable.
- `PRAGMA foreign_keys=ON`. Neither schema declares a foreign key, so
  this constrains nothing.
[*startup.foreign-keys-are-enabled]
- `PRAGMA busy_timeout=0`; SQL lock conflicts return busy immediately (§4.4).
[*startup.the-busy-timeout-is-twenty-five-seconds]
- The volatile database is attached (§3.3).

Each `database/sql` handle is limited to a single underlying connection.
[*startup.each-sql-handle-is-limited-to-one-underlying-connection]

## 3. attach the volatile store
[*startup.the-volatile-store-is-attached-as-schema-volatile]

Each hive gets a fresh tmpfs-backed SQLite database, attached as `volatile`
and put in WAL mode with private caches. Before opening hives, loregd
protects and locks `/run/loregd`, verifies tmpfs, and removes old volatile
stores left by a crash (§3.3). It never reuses a prior process's volatile
data.
[*startup.volatile-stores-are-fresh-and-use-tmpfs-wal]

## 4. establish the schema

If the database has no `schema_version` table, it is new: loregd creates
the persistent tables, creates the volatile tables, and stamps the schema
version, in one transaction.
[*startup.a-new-database-gets-its-schema-created-in-one-transaction]

If the version is present, it is compared against the version loregd
supports. A newer version aborts startup, and so does an older one
(§3.1).
[*startup.a-schema-version-mismatch-in-either-direction-aborts-startup]

## 5. first-boot root key

For each hive, look for a key with no parent. If none exists, this is the
hive's first boot: loregd generates a random 16-byte GUID for the root
key, builds the default hive-root security descriptor, and inserts the
root key record.
[*startup.a-hive-with-no-parentless-key-gets-a-generated-root-key]

The default root descriptor grants SYSTEM and Administrators full access
to the key, grants Authenticated Users read access, marks all three as
container-inheritable, and sets both owner and group to SYSTEM.
[*startup.the-default-hive-root-descriptor]

## 6. crash recovery

SQLite's own WAL recovery handles any transaction that was uncommitted
when the process died; it happens when the database is opened and needs
nothing from loregd.
[*startup.wal-recovery-is-sqlites-own-and-needs-nothing-from-loregd]

What loregd does on top of that is clean up **orphaned keys**; key
records that no path entry in any layer points at.
[*startup.an-orphaned-key-is-one-no-path-entry-points-at]
 These can
survive a crash between a key's creation and the creation of the path
entry naming it. In one transaction, loregd deletes the values belonging
to orphaned keys, then their blanket tombstones, then the key records
themselves.
[*startup.orphaned-keys-are-deleted-values-then-tombstones-then-records]

The hive root is exempt: it legitimately has no parent and no path entry
pointing at it, so orphan detection skips keys whose `parent_guid` is
null.
[*startup.orphan-detection-skips-keys-with-a-null-parent]

## 7. compute the maximum sequence number

For each hive, take the maximum `sequence` across the path entries,
values, and blanket tombstones; in both the persistent and the volatile
tables; then take the maximum across all hives.
[*startup.the-maximum-sequence-is-taken-across-every-table-and-every-hive]
That single global figure is what loregd reports at registration, so the
kernel can resume allocating sequence numbers above everything already
stored.

In practice the volatile tables are empty at this point in startup and
contribute nothing.

## 8. open the registry device
[*startup.the-registry-device-is-opened-at-dev-pkm-registry]

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

## 9. register the hives

Issue `REG_SRC_REGISTER` with every hive name, its root key GUID, and the
global maximum sequence number from step 7.
<!-- lint-disable-next-line line-length -->
[*startup.registration-sends-every-hive-name-root-guid-and-the-maximum-sequence]
The registration flags are zero: loregd registers global hives only and
never private ones.
[*startup.the-registration-flags-are-zero-so-hives-are-global]

## 10. signal readiness and serve
[*startup.readiness-then-the-signal-handler-then-the-request-loop]

loregd sends readiness to `NOTIFY_SOCKET` if it is set (§2.1). The complete
notification attempt has a one-second deadline. A failed attempt produces
a warning, not a startup failure. The daemon installs its termination
signal watcher (§2.3) and enters the request loop (§4.2).

## Backup and repair limits

loregd creates no automatic startup backup. Opening a hive prepares its
schema, root, orphan cleanup, and reader pool. Orphan cleanup deletes
unreferenced persistent records; it does not restore a backup or repair
arbitrary database corruption.

WAL recovery and `RSI_FLUSH` checkpointing create no separate recovery copy.
Read-only RSI transactions provide snapshots for higher-level backup
operations, but do not save those snapshots themselves. The
[kernel backup interface](~kernel/internals/lcs/backup-and-restore/backup)
is a separate operation.

The [command-line interface](~peios/loregd/startup/command-line#unsupported-recovery-switches)
provides no offline repair command. Database recovery requires a separately
verified backup and a procedure appropriate to that backup format.
