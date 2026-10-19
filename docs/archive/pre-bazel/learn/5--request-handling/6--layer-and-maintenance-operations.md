---
title: Layer and Maintenance Operations
description: Delete-layer and flush — the two operations that check the request's transaction id but cannot join it, taking the write connection directly.
---

Both operations in this section consult the request's transaction id before
doing anything — a read-only transaction may not mutate, and an unknown id
is refused (§4.3) — but neither can *join* one.
[*layer.both-operations-check-the-transaction-id-but-cannot-join-one]
Both take the hive's write
connection directly and commit work of their own, which is why a bound
transaction makes them decline rather than wait (§4.1).
[*layer.both-operations-take-the-write-connection-directly-and-commit-their-own-work]

## RSI_DELETE_LAYER [*layer.delete-layer-removes-every-entry-of-one-layer-and-reports-the-orphans]

Removes every entry belonging to one layer and reports the keys that the
removal left unreferenced.

The operation is applied to **every registered hive**, not to one
identified from the request, and the per-hive orphan sets are
concatenated into a single response array.
[*layer.delete-layer-is-applied-to-every-registered-hive]

For each hive, inside one `BEGIN IMMEDIATE` transaction, loregd first
computes the orphan set and then deletes:

```sql
-- GUIDs referenced by the layer being removed
SELECT DISTINCT target_guid FROM main.path_entries
WHERE layer = ? AND target_type = 0
UNION
SELECT DISTINCT target_guid FROM volatile.path_entries
WHERE layer = ? AND target_type = 0
```

minus the GUIDs referenced by any *other* layer, gathered the same way
with `layer != ?`. What remains is reachable only through the layer being
deleted, and is therefore orphaned by it.
[*layer.an-orphan-is-a-guid-referenced-only-by-the-layer-being-deleted]

```sql
DELETE FROM path_entries       WHERE layer = ?;
DELETE FROM [values]           WHERE layer = ?;
DELETE FROM blanket_tombstones WHERE layer = ?;
```

Both schemas are covered, and all six deletions run inside the same
transaction as the orphan computation, so nothing can be inserted between
the two steps.
[*layer.the-orphan-computation-and-the-deletions-share-one-transaction]
The orphaned GUIDs are evicted from the hive cache (§4.2)
and returned to the caller.
[*layer.orphaned-guids-are-evicted-from-the-hive-cache-and-returned]

The response array is sorted into ascending byte order (§5.2).
[*layer.delete-layer-returns-the-orphan-set-in-ascending-byte-order]

A contended write is reported as `RSI_TXN_BUSY`, the same distinction every
other write path draws: the operation declines if any transaction already
holds a hive's write connection, and again if a hive's own
`BEGIN IMMEDIATE` finds the database busy.
[*layer.delete-layer-reports-a-contended-write-as-txn-busy]
Other failures are `RSI_STORAGE_ERROR`.
[*layer.delete-layer-reports-other-failures-as-storage-error]

## RSI_FLUSH [*layer.flush-checkpoints-the-write-ahead-log-for-durability]

Forces the hive's write-ahead log to be checkpointed so that all
persistent data is durable on disk:

```sql
PRAGMA wal_checkpoint(TRUNCATE)
```

The request carries a hive **name** rather than a GUID.
[*layer.flush-identifies-its-hive-by-name-rather-than-guid]
It is matched case-insensitively against the registered hives by folded
name (§3.4).
[*layer.flush-matches-the-hive-name-case-insensitively-by-folded-name]
A name matching none returns `RSI_INVALID`.
[*layer.flush-on-an-unregistered-hive-name-returns-invalid]

Two conditions return `RSI_TXN_BUSY` instead of checkpointing:

- Any transaction is currently bound to the hive.
  [*layer.flush-declines-while-a-transaction-is-bound-to-the-hive] A
  checkpoint on a connection already held by a transaction would deadlock,
  so loregd declines immediately rather than waiting (§4.4).
- The checkpoint itself reports that it could not complete because the
  database was busy.
  [*layer.flush-returns-txn-busy-when-the-checkpoint-reports-the-database-busy]

The volatile store has no durability and is unaffected: nothing is
flushed, and nothing needs to be.
[*layer.flush-leaves-the-volatile-store-untouched]
