---
title: Terminology
description: The registry vocabulary this manual borrows from the kernel-side documentation, and the terms loregd adds.
---

The registry's own vocabulary; hive, key, value, layer, source, watch,
security descriptor, sequence number; is defined by the kernel-side
registry documentation and is used here unchanged.

The terms below are specific to loregd.

**Hive database.** The SQLite database file backing one hive. Each hive
registered by loregd has its own file, whose path is given on the command
line (§2.1). Referred to in SQL as the `main` schema.
[*term.each-hive-has-its-own-database-file]

**Volatile database.** The tmpfs-backed SQLite WAL database for one hive's
volatile keys, attached as `volatile`. It mirrors the persistent tables,
starts empty, and is never reused across daemon instances (§3.3).
[*term.the-volatile-database-is-transient-tmpfs-storage]

**Folded name.** The case-folded form of a key name, value name, or child
name, stored in a `_folded` column beside the canonical case-preserving
name and used for all case-insensitive comparison (§3.4).
[*term.a-folded-name-is-used-for-all-case-insensitive-comparison]

**Write connection.** The single SQLite connection per hive through which
every mutation passes. Its uniqueness is what serializes writes (§4.1).
[*term.every-mutation-passes-through-the-single-write-connection]

**Read pool.** The fixed set of connections serving reads that are not
part of a transaction, selected round-robin (§4.1).
[*term.the-read-pool-serves-non-transactional-reads-round-robin]

**Snapshot connection.** A dedicated connection opened for one read-only
transaction, pinning a point-in-time view of the hive database for that
transaction's lifetime (§4.3).
[*term.a-snapshot-connection-pins-a-point-in-time-view]

**Orphan.** A key record that no path entry in any layer points at. Orphans
are cleaned up at startup (§2.2) and are reported by `RSI_DELETE_LAYER` as
the keys its deletion left unreachable (§5.6).
[*term.an-orphan-is-a-key-record-no-path-entry-points-at]
