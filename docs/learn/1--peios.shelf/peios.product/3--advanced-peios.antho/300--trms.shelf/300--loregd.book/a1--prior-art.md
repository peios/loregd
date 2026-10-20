---
title: Prior Art
description: loregd is specified against SQLite rather than an abstract store; how it relates to SQLite, the Windows hive format, and RSI.
---

loregd combines SQLite storage with the Peios registry source protocol.


## SQLite

loregd's storage engine is SQLite, and it is specified against SQLite
rather than against an abstract store.
[*priorart.loregd-is-specified-against-sqlite-not-an-abstract-store]
 The
schema, the concurrency model, and the operational behavior all name
SQLite features directly: WAL mode for concurrent readers alongside a
serialized writer, transactions for atomicity,
`PRAGMA wal_checkpoint` for durability on demand, and its crash recovery
for restart after an unclean shutdown.

Both halves of a hive are SQLite. Persistent data lives in the database
file named on the command line; volatile data lives in a second,
tmpfs-backed WAL database attached to the same connections (§3.3). Using one
engine for both allows a transaction to cover both schemas. Per-hive
publication gates coordinate readers with commits. Attached WAL databases
do not provide crash-atomic commit across files (§4.3).

A different storage engine would have to supply equivalent transactional
and concurrency semantics. Nothing in the design forbids that, but
nothing accommodates it either.

## The Windows registry hive format

The Windows registry stores hives as binary REGF files managed directly
by the kernel's Configuration Manager. loregd departs from that model
completely: storage is a SQLite database managed by a SYSTEM
userspace daemon with `SeTcbPrivilege`, not a kernel-managed binary file, and
the kernel holds
no storage of its own at all.

What the two share is the data model; keys, values, security descriptors
which Peios inherits through the registry's kernel-side specification
rather than from the file format. The on-disk format has no relationship
to REGF whatsoever, and no REGF file can be read by loregd or written by
it.
[*priorart.no-regf-file-can-be-read-or-written-by-loregd]

## The registry source interface

loregd implements the RSI. The
[kernel source interface](~kernel/internals/lcs/sources/overview) describes
the kernel side; the RSI specification defines the shared wire contract.
The request-handling chapter maps those operations onto SQL against the
two stores.

The protocol specification owns the contract. This manual describes the
implementation and its limitations; it does not replace the protocol or
turn implementation details into requirements for other sources.
[*priorart.the-rsi-specification-wins-over-this-manual]
