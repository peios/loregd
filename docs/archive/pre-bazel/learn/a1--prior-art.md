---
title: Prior Art
description: loregd is specified against SQLite rather than an abstract store — how it relates to SQLite, the Windows hive format, and RSI.
---

## SQLite

loregd's storage engine is SQLite, and it is specified against SQLite
rather than against an abstract store.
[*priorart.loregd-is-specified-against-sqlite-not-an-abstract-store] The
schema, the concurrency model, and the operational behaviour all name
SQLite features directly: WAL mode for concurrent readers alongside a
serialised writer, savepoints and transactions for atomicity,
`PRAGMA wal_checkpoint` for durability on demand, and its crash recovery
for restart after an unclean shutdown.

Both halves of a hive are SQLite. Persistent data lives in the database
file named on the command line; volatile data lives in a second,
tmpfs-backed WAL database attached to the same connections (§3.3). Using one
engine for both is what makes a transaction spanning persistent and
volatile data atomic without any additional machinery.

A different storage engine would have to supply equivalent transactional
and concurrency semantics. Nothing in the design forbids that, but
nothing accommodates it either.

## The Windows registry hive format

The Windows registry stores hives as binary REGF files managed directly
by the kernel's Configuration Manager. loregd departs from that model
completely: storage is a SQLite database managed by an unprivileged
userspace daemon, not a kernel-managed binary file, and the kernel holds
no storage of its own at all.

What the two share is the data model — keys, values, security descriptors
— which Peios inherits through the registry's kernel-side specification
rather than from the file format. The on-disk format has no relationship
to REGF whatsoever, and no REGF file can be read by loregd or written by
it. [*priorart.no-regf-file-can-be-read-or-written-by-loregd]

## The Registry Source Interface

loregd implements the RSI, which is
[specified in PSPK](~peios/registry-source-interface/scope) rather than
here. That specification defines the operations, the message format, the
error model, and the obligations binding on any source; loregd's request
handling (chapter 5) is a mapping of those operations onto SQL against
the two stores.

Where this manual and the RSI specification disagree about wire
behaviour, the specification is correct and this manual has a bug — the
RSI is a contract with the kernel, and loregd is one implementation of
one side of it. [*priorart.the-rsi-specification-wins-over-this-manual]
