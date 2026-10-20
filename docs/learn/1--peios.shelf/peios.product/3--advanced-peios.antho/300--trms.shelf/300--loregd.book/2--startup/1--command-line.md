---
title: Command Line
description: loregd is configured entirely by its argument vector; the hive declarations it takes, and how they are validated.
---

loregd is configured entirely by its argument vector. It takes one or
more hive declarations, each naming a hive and the SQLite database file
that backs it:
[*cmdline.an-argument-declares-a-hive-and-the-database-that-backs-it]

```text
loregd <hive-name>=<database-path> [<hive-name>=<database-path> ...]
```

The placeholders name a hive and its absolute database path. peinit selects
`Machine=/var/state/loregd/Machine.hive` and
`Users=/var/state/loregd/Users.hive` for its registry service. The daemon
has no built-in database-path defaults.

Each argument is split at its **first** `=`, so a database path can
itself contain `=`.
[*cmdline.an-argument-is-split-at-its-first-equals-sign]

Every declared hive is registered with the kernel at startup (§2.2).
[*cmdline.every-declared-hive-is-registered-with-the-kernel]

## Argument validation
[*cmdline.an-invalid-invocation-is-rejected-with-a-non-zero-exit]

loregd rejects the invocation and exits with status 1 if any of
the following hold:

| Condition | Reason |
|---|---|
| No hive arguments at all | At least one hive is required. |
| An argument with no `=` | Not a hive declaration. |
| An empty hive name or empty path | Neither is meaningful. |
| A relative database path | Paths are required to be absolute. |
| A hive name containing `\`, `/`, or NUL | These are path separators and terminators in the registry namespace. |
| The hive name `CurrentUser`, in any case | Reserved by the kernel as a per-token alias; no source can claim it. |
| Two declarations of the same hive name | Duplicates are detected on the **folded** name, so `Machine` and `MACHINE` collide. |

[*cmdline.at-least-one-hive-argument-is-required]
[*cmdline.an-argument-without-an-equals-sign-is-rejected]
[*cmdline.an-empty-hive-name-or-path-is-rejected]
[*cmdline.a-database-path-must-be-absolute]
<!-- lint-disable-next-line words -->
[*cmdline.a-hive-name-may-not-contain-a-separator-or-nul]
[*cmdline.the-hive-name-currentuser-is-reserved]
[*cmdline.duplicate-hive-names-are-detected-on-the-folded-name]


Hive-name comparison is case-insensitive throughout; for duplicate
detection here, and for routing requests later; but the case as written
on the command line is preserved and is what loregd presents to the
kernel when it registers.
[*cmdline.hive-name-comparison-is-case-insensitive-but-the-case-is-preserved]

## Unsupported recovery switches

The argument parser accepts hive declarations only. It has no help, version,
inspection, restore, or clearing switches. In particular, `--inspector`,
`--recover-from-backup`, and `--dangerously-clear-database` fail validation
because they lack `=`.

[Startup backup and repair limits](~peios/loregd/startup/startup-sequence#backup-and-repair-limits)
describes the distinction between WAL recovery and a saved backup.

## Configuration

loregd has no configuration file and reads no configuration from the
registry.
[*cmdline.there-is-no-configuration-file-or-registry-configuration]
 This
is deliberate: loregd is the configuration store, and a
store that had to read its own configuration to start could not
start.

Everything about how it behaves comes from three places: the command-line
arguments above, the contents of the SQLite databases they name, and
compiled-in constants (§4.1).
<!-- lint-disable-next-line spelling -->
[*cmdline.behaviour-comes-from-arguments-databases-and-compiled-in-constants]

`NOTIFY_SOCKET` is the one environment variable loregd consults, and it
carries no behavioral setting.
[*cmdline.notify-socket-is-the-only-environment-variable-consulted]
 When
it is set, loregd treats it as a service-manager readiness socket: once
the hives are registered and the request loop is about to begin, it
connects and sends `READY=1\n`. Connection and write share a one-second
deadline. Failure produces a warning and startup continues.
[*cmdline.readiness-is-sent-as-ready-equals-1-before-the-request-loop]
When it is unset, the step is skipped.
[*cmdline.readiness-is-skipped-when-notify-socket-is-unset]

loregd writes ordinary progress to standard output and faults to standard
error.
[*cmdline.progress-goes-to-standard-output-and-faults-to-standard-error]
peinit captures both streams during Phase 1: if loregd fails before
readiness, peinit relays the captured diagnostic output to the console; after
the log service starts, the pre-eventd buffer carries both streams into
eventd.
This keeps early-boot failures visible without bypassing the system log.
