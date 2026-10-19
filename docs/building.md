# Building and Testing

Bazel builds loregd and its packages from the declared Go dependency graph.
The repository launcher selects the version in `.bazelversion`; Go and
module versions belong to the Bazel module configuration and lockfile.
Do not change generated dependency metadata independently of its inputs.

## Development host

Use Linux for the development build. Run the launcher from the repository
root. The first build needs access to the declared tool and dependency
archives; later actions reuse Bazel's cache.

Build the daemon and run the test targets:

```sh
./bazel build //:loregd
./bazel test //...
```

The tests exercise argument validation, SQLite state, case folding, security
descriptors, RSI framing, and handlers. Test helpers replace native kernel
operations where needed. A successful host test does not establish that
KACS protection, kernel registration, or service startup works on Peios.

## Packages

Build all package outputs together:

```sh
./bazel build //:packages
```

`bazel-bin/packages.store` contains manifests and content-addressed blobs.
The package set is:

| Package | Contents |
|---|---|
| `dev.peios.loregd` | Daemon, man page, and protected state-directory payload |
| `dev.peios.loregd-debuginfo` | Debug information matching the runtime executable |
| `dev.peios.loregd-debugsource` | Go dependency sources and the pinned standard-library source set |
| `dev.peios.loregd-source` | Repository source and separate dependency source tree |

Package provenance sidecars record the `registryd` claim, dependencies,
and state-directory security descriptor and mode `0700`. The canonical
manifest format is unchanged. Those sidecar declarations do not establish
that a package installer applies the claim or security metadata.

The source package includes dependencies under `/usr/src/loregd-dependencies`.
Debug source includes the pinned standard-library source set, not only the
standard-library files selected by this executable.

`build/version.bzl` retains version `0.21.13`; package revision `-1` is a
packaging revision. Building those artifacts does not create a release or
establish a native qualification result. Keep each runtime package with its
matching debug and source packages.

## Native qualification

For a native package build, run inside a prepared Peios userspace with
`PEIOS_RUSTC` set to the Peios Rust compiler used for the PPK tools:

```sh
./bazel build --config=peios //:packages
```

For an offline build, acquire a snapshot on a connected development host:

```sh
tools/offline-build acquire /absolute/path/to/snapshot
```

Make that snapshot and the same source revision available inside the prepared
userspace, then run `tools/offline-build build /path/to/snapshot /path/to/work`.
The helper checks the module lock, disables downloads, and uses a separate
writable work directory. The caller supplies isolation and the native tools;
the helper does not provision a Peios system.

The daemon uses the same pinned pure-Go SDK as the development build; it
does not require a separate native Go compiler. Native PPK tooling and
package creation are distinct from runtime qualification.

Run native qualification in a disposable Peios guest, using isolated hive
files. The daemon applies KACS descriptors to its runtime directory,
requires tmpfs, opens `/dev/pkm_registry`, and negotiates lifetime version 1.
An ordinary Linux host cannot provide those interfaces.

Check these behaviors before treating a runtime build as qualified:

- Startup as SYSTEM with `SeTcbPrivilege`, registration, and readiness.
- Persistent writes across restart and empty volatile state after restart.
- Read-only snapshots while writers remain active.
- Busy replies, request deadlines, transaction leases, and shutdown cleanup.
- Boot supervision through peinit and the packaged `registryd` provider.

Record the kernel revision, package hashes, guest setup, logs, and observed
results with the qualification work. Do not run a second test daemon against
live hives or the live `/run/loregd` directory.

## Compatibility details

Argument errors exit with status 1, matching other startup failures. The
interface has no `--help` or `--version` option. This is an existing CLI
compatibility exception to the house usage-error convention; documentation
and package smoke tests describe that behavior without introducing flags.

The Unicode table is generated from Unicode 16.0.0 simple-fold mappings.
Its source checksum is recorded in `internal/fold/casefold_table.go`. The
kernel repository owns `peios/generate-casefold-table.py`; update both
outputs together when changing the folding inputs. Schema version 1 does
not migrate folded columns written by older implementations.
