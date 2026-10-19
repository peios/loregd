---
title: Overview
description: loregd is the local registry source — it holds hives in SQLite and serves them to the kernel over RSI. Where it sits, and what this manual covers.
---

**loregd** — the Local Registry Daemon — is the primary registry source
for Peios. It holds one or more registry hives in SQLite databases and
serves them to the kernel's registry subsystem over the Registry Source
Interface (RSI).
[*overview.loregd-holds-hives-in-sqlite-and-serves-them-over-rsi]

loregd is not architecturally special. Any process that implements the
RSI contract can serve as a registry source, and the kernel is
source-agnostic: it does not know or care that the process answering for
a hive keeps its data in SQLite.
[*overview.any-process-implementing-rsi-can-be-a-registry-source]

What makes loregd special is operational. It is the source that provides
the `Machine\` and `Users\` hives at boot, which puts it on the critical
path to a running system — if loregd does not come up, very little else
does. [*overview.loregd-provides-the-machine-and-users-hives-at-boot]

The `dev.peios.loregd` package installs the daemon, its `registryd` role
provider, and the protected empty state directory. Debugging support is split
into `dev.peios.loregd-debuginfo` and `dev.peios.loregd-debugsource`; the
matching build inputs are published as `dev.peios.loregd-source`.
[*overview.the-loregd-package-installs-the-daemon-and-its-role-provider]

peinit owns the compiled-in Phase-1 service definition, so Loregd does not
ship a second service seed that could compete with it.
[*overview.loregd-ships-no-service-seed-of-its-own]

## Where loregd sits

The registry is split across a trust boundary. The kernel side owns the
namespace, the layer model, access checks, watches, and transactions; it
holds no storage of its own. The source side owns bytes on disk and
answers questions about them.
[*overview.the-registry-is-split-across-a-trust-boundary] loregd is a
source, and everything in this manual describes the lower half of that
split.

Three consequences run through the whole design:

- **loregd stores; it does not decide.** It never resolves layers, never
  filters results by visibility, and never applies a security descriptor.
  It returns every layer entry it holds and lets the kernel work out
  which one wins. The security descriptors in its tables are opaque
  payload to it. [*overview.loregd-stores-it-does-not-decide]
- **GUIDs come from the kernel.** loregd does not mint key identities. It
  records the GUID it is given, and uses it as the primary key.
  [*overview.guids-come-from-the-kernel]
- **Sequence numbers come from the kernel.** loregd stores them and
  reports the maximum it holds at registration, but never allocates one.
  [*overview.sequence-numbers-come-from-the-kernel]

## What this manual covers

The command line and startup sequence; the SQLite schema backing a hive
and the tmpfs-backed store backing volatile keys; the connection model,
write serialisation, and how requests are dispatched; and the handling of
each RSI operation, including transactions and enumeration ordering.

The kernel half of the registry — the namespace, layers, watches, access
control — is [chapter 5 of the Peios Kernel TRM](~peios/lcs/overview),
and the RSI wire protocol itself is
[specified in PSPK](~peios/registry-source-interface/scope). Key schemas
belong to the subsystems that own them, and loregd's supervision as a
service belongs to the init system's manual.
