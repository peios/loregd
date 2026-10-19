---
title: Command Line
description: loregd is configured entirely by its argument vector — the hive declarations it takes, and how they are validated.
---

loregd is configured entirely by its argument vector. It takes one or
more hive declarations, each naming a hive and the SQLite database file
that backs it:
[*cmdline.an-argument-declares-a-hive-and-the-database-that-backs-it]

```
loregd <HiveName>=<DatabasePath> [<HiveName>=<DatabasePath> ...]
```

For example:

```
loregd Machine=/var/state/registry/machine.regdb Users=/var/state/registry/users.regdb
loregd Machine=/var/state/registry/machine.regdb Users=/var/state/registry/users.regdb Roles=/var/state/registry/roles.regdb
```

Each argument is split at its **first** `=`, so a database path may
itself contain `=`.
[*cmdline.an-argument-is-split-at-its-first-equals-sign]

Every declared hive is registered with the kernel at startup (§2.2).
[*cmdline.every-declared-hive-is-registered-with-the-kernel]

## Argument validation [*cmdline.an-invalid-invocation-is-rejected-with-a-non-zero-exit]

loregd rejects the invocation and exits with a non-zero status if any of
the following hold:

| Condition | Reason |
|---|---|
| No hive arguments at all | At least one hive is required. [*cmdline.at-least-one-hive-argument-is-required] |
| An argument with no `=` | Not a hive declaration. [*cmdline.an-argument-without-an-equals-sign-is-rejected] |
| An empty hive name or empty path | Neither is meaningful. [*cmdline.an-empty-hive-name-or-path-is-rejected] |
| A relative database path | Paths are required to be absolute. [*cmdline.a-database-path-must-be-absolute] |
| A hive name containing `\`, `/`, or NUL | These are path separators and terminators in the registry namespace. [*cmdline.a-hive-name-may-not-contain-a-separator-or-nul] |
| The hive name `CurrentUser`, in any case | Reserved by the kernel as a per-token alias; no source may claim it. [*cmdline.the-hive-name-currentuser-is-reserved] |
| Two declarations of the same hive name | Duplicates are detected on the **folded** name, so `Machine` and `MACHINE` collide. [*cmdline.duplicate-hive-names-are-detected-on-the-folded-name] |

Hive-name comparison is case-insensitive throughout — for duplicate
detection here, and for routing requests later — but the case as written
on the command line is preserved and is what loregd presents to the
kernel when it registers.
[*cmdline.hive-name-comparison-is-case-insensitive-but-the-case-is-preserved]

## Unsupported recovery switches

In the source-checked implementation at `3cb9768` (1 October 2026),
`--inspector`, `--recover-from-backup` and `--dangerously-clear-database`
are not implemented switches. The [argument parser](https://github.com/peios/loregd/blob/3cb9768b0586ab6563f3b3c50623998624926a8d/internal/config/config.go#L18-L33)
accepts hive declarations only; each of these spellings lacks `=` and is
rejected as a malformed declaration. They are not offline inspection,
restoration or clearing commands.

See [startup backup and repair limits](~peios/loregd/startup/startup-sequence#backup-and-repair-limits)
before treating startup recovery as a way to repair a database. Identify the
installed provider and revision before choosing any recovery procedure.

## Configuration

loregd has no configuration file and reads no configuration from the
registry.
[*cmdline.there-is-no-configuration-file-or-registry-configuration] This
is deliberate: loregd *is* the configuration store, and a
store that had to read its own configuration in order to start could not
start.

Everything about how it behaves comes from three places: the command-line
arguments above, the contents of the SQLite databases they name, and
compiled-in constants (§4.1).
[*cmdline.behaviour-comes-from-arguments-databases-and-compiled-in-constants]

`NOTIFY_SOCKET` is the one environment variable loregd consults, and it
carries no behavioural setting.
[*cmdline.notify-socket-is-the-only-environment-variable-consulted] When
it is set, loregd treats it as a service-manager readiness socket: once
the hives are registered and the request loop is about to begin, it
connects and sends `READY=1`.
[*cmdline.readiness-is-sent-as-ready-equals-1-before-the-request-loop]
When it is unset, the step is skipped.
[*cmdline.readiness-is-skipped-when-notify-socket-is-unset]

loregd writes ordinary progress to standard output and faults to standard
error.
[*cmdline.progress-goes-to-standard-output-and-faults-to-standard-error]
peinit captures both streams during Phase 1: if loregd fails before
readiness, peinit relays the captured diagnostic output to the console; after
the log service starts, the pre-eventd buffer carries both streams into eventd.
This keeps early-boot failures visible without bypassing the system log.
