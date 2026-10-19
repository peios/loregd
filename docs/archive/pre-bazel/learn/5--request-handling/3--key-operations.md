---
title: Key Operations
description: The four key operations and the SQL behind each — create, read, write and drop.
---

## RSI_CREATE_KEY [*key.create-key-inserts-a-new-key-row]

The request's volatile flag selects the target table (§5.1). For a
persistent key:

```sql
INSERT INTO keys
    (guid, name, name_folded, parent_guid, sd, volatile, symlink,
     last_write_time)
VALUES (?, ?, fold(?), ?, ?, 0, ?, ?)
```

`last_write_time` is not carried in the request. loregd sets it to the
current wall-clock time in Unix nanoseconds at insertion.
[*key.last-write-time-is-set-by-loregd-at-insertion]

Uniqueness comes from the target table's primary key on `guid`, surfaced
as `RSI_ALREADY_EXISTS`.
[*key.a-duplicate-guid-in-the-target-table-returns-already-exists] That
key binds one schema, so loregd also asks the *other* store whether it
holds the GUID before inserting, and answers `RSI_ALREADY_EXISTS` if it
does. [*key.a-guid-held-by-the-other-store-also-returns-already-exists]
Without that check a GUID could come to exist in both stores, where
metadata reads would resolve it to the `main` row — the reading query
takes the first row of a `UNION ALL` that puts `main` first — leaving the
volatile row unreachable while it still occupied its GUID.

The new GUID is added to the hive cache immediately, before the enclosing
transaction commits, with an abort hook to remove it if that transaction
rolls back (§4.2).
[*key.the-new-guid-is-cached-before-commit-and-dropped-on-rollback]

An unresolvable parent GUID returns `RSI_NOT_FOUND`, after a fallback
check of the registered hives' root GUIDs.
[*key.an-unresolvable-parent-guid-returns-not-found]

## RSI_READ_KEY [*key.read-key-returns-the-stored-key-metadata]

```sql
SELECT name, parent_guid, sd, volatile, symlink, last_write_time
FROM main.keys WHERE guid = ?
UNION ALL
SELECT name, parent_guid, sd, volatile, symlink, last_write_time
FROM volatile.keys WHERE guid = ?
LIMIT 1
```

`RSI_NOT_FOUND` if the GUID is in neither store,
[*key.read-key-returns-not-found-for-a-guid-in-neither-store] and likewise
if it resolves to no hive.
[*key.read-key-returns-not-found-when-the-guid-resolves-to-no-hive] The
`volatile` field in the response is the stored column value.
[*key.the-volatile-field-returned-is-the-stored-column-value]

## RSI_WRITE_KEY [*key.write-key-updates-the-two-mutable-fields]

Updates the two mutable fields of a key, selected by a field mask:

| Bit | Value | Field |
|---|---|---|
| 0 | `0x01` | `sd` [*key.mask-bit-0-selects-the-security-descriptor] |
| 1 | `0x02` | `last_write_time` [*key.mask-bit-1-selects-the-last-write-time] |

Valid masks are therefore `0x00`, `0x01`, `0x02` and `0x03`. Any other bit
set returns `RSI_INVALID` — it indicates an attempt to modify an immutable
field. [*key.a-mask-with-any-other-bit-set-returns-invalid]

loregd builds one `UPDATE` from the mask, setting only the named fields:
[*key.the-update-sets-only-the-fields-the-mask-names]

```sql
-- mask 0x03
UPDATE keys SET sd = ?, last_write_time = ? WHERE guid = ?
```

A mask of `0x00` names no fields and acts as an existence check, returning
`RSI_OK` or `RSI_NOT_FOUND`.
[*key.a-zero-mask-acts-as-an-existence-check] An update that matches no
row also returns `RSI_NOT_FOUND`.
[*key.an-update-matching-no-row-returns-not-found]

Outside a transaction the update is a single auto-committed statement.
[*key.write-key-outside-a-transaction-is-one-auto-committed-statement]
Inside one it runs on the transaction's connection.
[*key.write-key-inside-a-transaction-uses-its-connection]

## RSI_DROP_KEY [*key.drop-key-purges-a-guid-from-four-tables-in-each-schema]

Purges every trace of a GUID from both stores — four tables in each
schema:

```sql
DELETE FROM keys               WHERE guid = ?;
DELETE FROM path_entries       WHERE target_guid = ?;
DELETE FROM [values]           WHERE key_guid = ?;
DELETE FROM blanket_tombstones WHERE key_guid = ?;
```

Outside a transaction, the eight statements are wrapped in a
`BEGIN IMMEDIATE` transaction of their own so the purge is atomic.
[*key.drop-key-outside-a-transaction-wraps-the-purge-in-begin-immediate]
Inside one, they run on the transaction's connection.
[*key.drop-key-inside-a-transaction-uses-its-connection]

Dropping a GUID that does not exist returns `RSI_OK`;
[*key.dropping-a-guid-that-does-not-exist-returns-ok] so does one that
resolves to no hive.
[*key.dropping-a-guid-that-resolves-to-no-hive-returns-ok] The operation
is idempotent. [*key.drop-key-is-idempotent] The GUID is evicted from the
hive cache (§4.2). [*key.drop-key-evicts-the-guid-from-the-hive-cache]
