---
title: Diagnosing Registry Storage Failures
type: reference
description: Interpreting loregd startup failures, busy responses, storage
  errors, and recovery limits.
---

Diagnose a failure from the daemon's captured error output and the failing
operation. Process liveness alone does not establish registry health: loregd
can remain registered while SQLite returns storage errors.

## Startup failures

The failure stage identifies the relevant checks:

| Stage | Check |
|---|---|
| Argument parsing | Every hive declaration contains a nonempty name and absolute path; folded names are unique. |
| Runtime directory | `/run/loregd` is tmpfs-backed and available to the SYSTEM daemon; another instance does not hold its lock. |
| Database preparation | Parent paths are writable, SQLite can use WAL, and the database has supported schema version 1. |
| Device access | The Peios registry device exists and the caller has `SeTcbPrivilege`. |
| Negotiation | The kernel supports RSI lifetime extension version 1. |
| Registration | The hive names and their global registration are accepted by the kernel. |

Argument and startup failures exit with status 1. Readiness notification
failure is a warning and does not terminate serving.

## Busy and storage responses

`RSI_TXN_BUSY` can indicate a competing write transaction, SQLite lock
contention, a blocked checkpoint, or an expired request wait. A read-only
snapshot does not own the write connection, but it can keep WAL pages that
a truncate checkpoint needs to release.

`RSI_STORAGE_ERROR` includes database failures and invalid transaction state.
An interrupted bound transaction requires abort; using its ID again does not
silently start a replacement transaction. A request timeout does not prove
that an already-admitted commit failed. See
[waiting and contention](~peios/loregd/concurrency/waiting-and-contention).

## Recovery limits

Startup performs SQLite WAL recovery and removes orphaned persistent keys.
It does not restore a backup, rebuild case-folded columns, or repair an
arbitrary corrupt database. The schema check is not a full integrity check:
it does not validate every existing table definition or detect multiple
version rows.

Preserve the affected files and diagnostics before attempting repair. Use
a verified recovery procedure for the actual backup format. The daemon
has no `--inspector`, `--recover-from-backup`, or
`--dangerously-clear-database` switch.

## Separate a source outage from a denied operation

Record the exact operation, key, and error. `reg` status 3 means access
denied; status 5 includes source and syscall failures. Do not replace a
source database merely because a setting is missing or unreadable.

The `registryd` role names the selected source provider, not another daemon
beside loregd. Its startup paths come from Peinit's boot policy. Inspect the
installed arguments instead of treating a path from an example as a default.
Do not start a second provider against the live databases to investigate.

On source disconnection, handles remain but operations needing source data
fail. Re-registration produces watch overflow recovery: clients must reread
the state they need. A timed-out mutation or commit can already have reached
storage, so inspect live state before repeating it after recovery.

LCS owns access checks, routing, layers, and watches. That does not make a
source untrusted: access checks use descriptors returned by the source, and
structural validation cannot detect every well-formed false descriptor or
entry. Protect the source process, launch configuration, and backing files
as part of the trusted computing base.
