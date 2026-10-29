# Building and Testing

Use pekit to build loregd and run its tests. Pekit compiles each Go package
on its own, so a change rebuilds only the packages it reaches. The
supported development host is x86-64 Linux.

## Before you start

You need pekit and Git. Everything else is pinned and fetched by pekit on
the first build: Go 1.26.8, the busybox build environment, the Bootlin C
toolchain the race detector links with, and the modules `go.sum` names. The
first build needs network access. Later builds find what they need in
pekit's store, and every step runs without network access in a sandbox that
holds only its declared inputs.

The build files use the gcslib libraries named in `workspace.gcsl`.
`pekit lock` pins their commits in `pekit.lock`.

## Build

```sh
pekit build loregd
```

Pekit prints where the daemon is, under `pekit-out/loregd/`. It is pure Go,
a position-independent executable bound at load time, with a GNU build ID
derived from Go's, as the Bazel build made it.

## Test

```sh
pekit test
```

Each package's tests are a test of their own, named after the package:
`main` for the daemon's own, then `config`, `device`, `fold`, `handler`,
`hivedb`, `rsi`, and `sd`. Two more tests check the whole module:

| Test | Check |
|---|---|
| `vet` | `go vet` of every package, as Bazel's `nogo` checked it |
| `race` | Every package's tests again, with the race detector |

`fold` is given Unicode 16.0.0's `CaseFolding.txt`, pinned by its SHA-256.
It checks the generated table against that file for every scalar.

A test whose inputs did not change is found in the store and not run again.
The tests exercise argument validation, SQLite state, case folding,
security descriptors, RSI framing, and handlers. Test helpers replace
native kernel operations where needed. A successful host test does not
establish that KACS protection, kernel registration, or service startup
works on Peios.

For the full contributor check, install the pinned PSC tools into a
directory outside the repository, then run the shared entry point:

```sh
python3 tools/prepare-style-tools.py "$HOME/.cache/loregd-style"
export PEIOS_STYLE_BIN="$HOME/.cache/loregd-style/bin"
tools/check
```

`tools/check` checks owned source formatting and lint with gofmt, Ruff,
ShellCheck, shfmt, and Prettier, then runs `pekit test`. The style checks
use the host's `gofmt`. The tool versions and hashes are recorded in
`tools/style-tools.lock.json`. GitHub's workflow runs the style checks
alone; builds and tests run with pekit.

## Modules

Keep module versions in `go.mod` and `go.sum`, and change them with the go
command. `go.pins` records the SHA-256 of each module file `go.sum` names,
which pekit fetches it by. After a change to `go.sum`, write it again:

```sh
pekit run pin_modules
```

The script fetches each file from the Go module proxy with the host's
`curl`. Review a change to `go.pins` as a change to `go.sum` is reviewed.
A build fails, naming each file, when `go.pins` lacks a file `go.sum`
names. The go command also checks every module against `go.sum`.

## How the build is planned

`build.gcsl` plans the module with the gcslib go library's `workspace`.
The go command lists what each program and test needs, package by package,
and pekit compiles each package as a step. Planning reads the sources'
imports, as the go command does, so an edit plans again. Only the packages
whose files or dependencies changed are compiled again.

## Not built with pekit yet

Packages, the offline build, and builds with the native Peios toolchain
are not yet built with pekit. The package definitions and native build
settings from the Bazel build are kept outside this repository until they
are. The last Bazel release version was `0.21.13`.

The integration suite runs against a booted Peios image through
`peios-qualify`, as `tests/integration/README.md` describes. It is not part
of `pekit test`.

## Native qualification

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
