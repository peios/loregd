---
title: Operating loregd
type: concept
description: Startup requirements, readiness, persistence, and shutdown of
  the local registry source.
---

loregd supplies the local registry hives that Peios needs during boot.
peinit selects the hive names and database paths and supervises the daemon.
You normally use the registry through its kernel interfaces rather than
starting loregd yourself.

## Startup requirements

The daemon needs these resources before it can serve the registry:

- A SYSTEM token and `SeTcbPrivilege` for native protection and RSI access.
- A Peios kernel with RSI lifetime extension version 1.
- Writable persistent database paths supplied as hive declarations.
- A tmpfs-backed `/run/loregd` directory that it can protect and lock.

peinit supplies `Machine=/var/state/loregd/Machine.hive` and
`Users=/var/state/loregd/Users.hive`. loregd itself has no path defaults and
reads no configuration file or registry configuration. The
[command-line reference](~peios/loregd/startup/command-line) describes the
accepted declarations.

The runtime lock excludes a second daemon using `/run/loregd`. Do not use
that directory or a live hive for a development test. A daemon restart
removes volatile registry data even if the operating system has not rebooted.

## Readiness and logs

If `NOTIFY_SOCKET` is set, loregd sends `READY=1` after registration. The
complete notification attempt has a one-second deadline. Failure is logged
and serving continues, so a missing readiness message does not by itself
prove that registration failed.

Progress goes to standard output and failures to standard error. peinit
captures these streams during early boot. The daemon does not require
eventd to report an early startup error.

## Persistent and volatile data

Persistent data lives in each named SQLite hive. A hive can have adjacent
WAL and shared-memory files while open. Volatile data lives in fresh,
per-hive databases under `/run/loregd` and is discarded on every restart.

Do not treat the main SQLite file alone as a live backup: committed data
can remain in its WAL. loregd supports read-only RSI snapshots for
higher-level backups, but creates no automatic startup backup. See
[backup and repair limits](~peios/loregd/startup/startup-sequence#backup-and-repair-limits).

## Stopping the daemon

`SIGTERM` and `SIGINT` close the source device. loregd drains requests,
releases transaction state, closes hive connections, and removes volatile
stores. The kernel marks the served hives unavailable when their source
disconnects. This can affect sign-in and other services that use the registry.
