---
title: Overview
description: loregd is the local registry source; it holds hives in SQLite and serves them to the kernel over RSI. Where it sits, and what this manual covers.
---

**loregd**; the Local Registry Daemon; is the primary registry source
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
path to a running system; if loregd does not come up, very little else
does.
[*overview.loregd-provides-the-machine-and-users-hives-at-boot]

The `dev.peios.loregd` package contains the daemon, man page, and empty
state directory. Its provenance metadata declares the `registryd` provider
and the directory protection; installer support for applying that metadata
is a separate integration requirement. Debugging support is split
into `dev.peios.loregd-debuginfo` and `dev.peios.loregd-debugsource`; the
matching build inputs are published as `dev.peios.loregd-source`.
[*overview.the-loregd-package-installs-the-daemon-and-its-role-provider]

peinit owns the compiled-in Phase-1 service definition, so loregd does not
ship a second service seed that could compete with it.
[*overview.loregd-ships-no-service-seed-of-its-own]

## Where loregd sits

The registry is split across a trust boundary. The kernel side owns the
namespace, the layer model, access checks, watches, and transactions; it
holds no storage of its own. The source side owns bytes on disk and
answers questions about them.
[*overview.the-registry-is-split-across-a-trust-boundary]
 loregd is a
source, and everything in this manual describes the lower half of that
split.

Three consequences run through the whole design:

- **loregd stores; it does not decide.** It never resolves layers, never
  filters results by visibility, and never applies the stored registry
  security descriptor.
  It returns every layer entry it holds and lets the kernel work out
  which one wins. The security descriptors in its tables are opaque
  payload to it.
[*overview.loregd-stores-it-does-not-decide]
- **Key creation requests carry kernel-assigned GUIDs.** loregd records
  each GUID as the primary key. On first boot, loregd generates the root
  GUID for an empty hive before registering that hive.
[*overview.guids-come-from-the-kernel]
- **Sequence numbers come from the kernel.** loregd stores them and
  reports the maximum it holds at registration, but never allocates one.
[*overview.sequence-numbers-come-from-the-kernel]

## What this manual covers

The command line and startup sequence; the SQLite schema backing a hive
and the tmpfs-backed store backing volatile keys; the connection model,
write serialization, and how requests are dispatched; and the handling of
each RSI operation, including transactions and enumeration ordering.

The [kernel registry manual](~kernel/internals/lcs/start-here/overview)
describes namespaces, layers, watches, and access control. The
[kernel source interface](~kernel/internals/lcs/sources/overview) describes
the other end of the RSI connection. Key schemas belong to their owning
subsystems; service supervision belongs to peinit.

## Compatibility and scope

This manual describes loregd 0.21.13 and SQLite schema version 1. The daemon
requires the kernel RSI lifetime extension version 1 before registering any
hive. It does not fall back to an earlier lifetime protocol.

The daemon registers global hives only. A transaction binds to one hive;
layer deletion visits each registered hive independently. The daemon does
not import Windows REGF files or migrate an older SQLite schema.
