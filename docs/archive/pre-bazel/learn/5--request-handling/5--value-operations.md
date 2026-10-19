---
title: Value Operations
description: Querying, setting and deleting value entries, conditional writes, and setting a blanket tombstone.
---

## RSI_QUERY_VALUES [*value.query-values-returns-one-value-or-all-of-a-keys-values]

Returns every layer's entry for one value, or for all of a key's values
when the request sets the query-all flag:

```sql
-- single value
SELECT name, layer, type, data, sequence
FROM main.[values] WHERE key_guid = ? AND name_folded = ?
UNION ALL
SELECT name, layer, type, data, sequence
FROM volatile.[values] WHERE key_guid = ? AND name_folded = ?

-- query all
SELECT name, layer, type, data, sequence
FROM main.[values] WHERE key_guid = ?
UNION ALL
SELECT name, layer, type, data, sequence
FROM volatile.[values] WHERE key_guid = ?
```

The response also carries the key's blanket-tombstone state:
[*value.query-values-also-returns-the-blanket-tombstone-state]

```sql
SELECT layer, sequence
FROM main.blanket_tombstones WHERE key_guid = ?
UNION ALL
SELECT layer, sequence
FROM volatile.blanket_tombstones WHERE key_guid = ?
```

Value entries and blanket tombstones are both sorted (§5.2).
[*value.query-values-sorts-value-entries-and-blanket-tombstones]

An unresolvable GUID returns `RSI_NOT_FOUND`.
[*value.query-values-on-an-unresolvable-guid-returns-not-found]
An existing key with no values returns `RSI_OK` with empty arrays.
[*value.query-values-on-a-key-with-no-values-returns-empty-arrays]

## RSI_SET_VALUE [*value.set-value-writes-one-layers-entry-for-a-value]

The store containing the key selects the target table (§5.1).
[*value.set-value-targets-the-keys-store]
A key GUID in neither store returns `RSI_NOT_FOUND`.
[*value.set-value-on-an-unknown-key-guid-returns-not-found]

```sql
INSERT OR REPLACE INTO [values]
    (key_guid, name, name_folded, layer, type, data, sequence)
VALUES (?, ?, fold(?), ?, ?, ?, ?)
```

### Conditional writes [*value.a-non-zero-expected-sequence-makes-the-write-a-compare-and-swap]

When the request carries a non-zero `expected_sequence`, the write is a
compare-and-swap. loregd reads the current entry's sequence and writes only
if it matches:

```sql
SELECT sequence FROM [values]
WHERE key_guid = ? AND name_folded = ? AND layer = ?
```

If the row is absent, or its sequence differs, the operation returns
`RSI_CAS_FAILED` and writes nothing.
[*value.a-failed-compare-and-swap-returns-cas-failed-and-writes-nothing]

Outside a transaction, the check and the write are wrapped in their own
`BEGIN IMMEDIATE` transaction so no other writer can interleave.
[*value.an-unbound-conditional-write-wraps-itself-in-begin-immediate]
If that transaction cannot begin because the database is busy, the
operation returns `RSI_TXN_BUSY`.
[*value.a-conditional-write-that-cannot-begin-its-transaction-returns-txn-busy]
Inside a transaction, the caller's transaction already provides the
isolation.
[*value.a-conditional-write-inside-a-transaction-relies-on-the-callers-isolation]

## RSI_DELETE_VALUE_ENTRY [*value.delete-value-entry-removes-one-layers-entry-from-both-stores]

Removes one layer's entry for one value, from both stores:

```sql
DELETE FROM [values]
WHERE key_guid = ? AND name_folded = ? AND layer = ?
```

No rows-affected check, so deleting an absent entry succeeds.
[*value.deleting-an-absent-value-entry-succeeds]
A GUID that resolves to no hive returns `RSI_NOT_FOUND`.
[*value.delete-value-entry-on-an-unresolvable-guid-returns-not-found]

## RSI_SET_BLANKET_TOMBSTONE [*value.a-blanket-tombstone-masks-every-value-in-lower-layers]

Sets or clears the tombstone that masks every value a key holds in lower
layers. The store containing the key selects the target table (§5.1).
[*value.set-blanket-tombstone-targets-the-keys-store]
A GUID in neither returns `RSI_NOT_FOUND`.
[*value.set-blanket-tombstone-on-an-unknown-key-guid-returns-not-found]

```sql
-- set
INSERT OR REPLACE INTO blanket_tombstones (key_guid, layer, sequence)
VALUES (?, ?, ?)

-- clear
DELETE FROM blanket_tombstones WHERE key_guid = ? AND layer = ?
```
