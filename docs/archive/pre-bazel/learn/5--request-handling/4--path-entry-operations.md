---
title: Path Entry Operations
description: Lookup, create, hide, delete and enumerate — the operations over the per-layer entries that give a key its names.
---

## RSI_LOOKUP [*entry.lookup-returns-every-layers-entry-for-one-child-name]

Returns every layer's entry for one child name under one parent, together
with metadata for the keys those entries point at.

```sql
SELECT layer, target_type, target_guid, sequence
FROM main.path_entries
WHERE parent_guid = ? AND child_name_folded = ?
UNION ALL
SELECT layer, target_type, target_guid, sequence
FROM volatile.path_entries
WHERE parent_guid = ? AND child_name_folded = ?
```

HIDDEN entries are returned as entries but contribute no metadata GUID.
[*entry.lookup-returns-hidden-entries-with-no-metadata-guid]
For each distinct non-HIDDEN `target_guid`, loregd fetches the key's
metadata — one query per GUID — and emits the blocks in ascending GUID
order (§5.2).
[*entry.lookup-emits-one-metadata-block-per-distinct-target-guid]

loregd does no layer filtering and no resolution: every entry it holds is
returned, and choosing between them is the kernel's job.
[*entry.lookup-does-no-layer-filtering-and-no-resolution]

An unresolvable parent GUID returns `RSI_NOT_FOUND`.
[*entry.lookup-on-an-unresolvable-parent-returns-not-found]

If a path entry names a `target_guid` for which no key record exists,
loregd drops that entry from the response and returns `RSI_OK`, so the
child reads as absent. [*entry.lookup-drops-an-entry-whose-key-record-is-missing]
This is reachable in ordinary operation: the kernel
issues `RSI_CREATE_ENTRY` before `RSI_CREATE_KEY`, so a lookup landing
between the two sees an entry whose key has not yet been written, and
failing the request would make a routine race fatal to the caller.

## RSI_CREATE_ENTRY [*entry.create-entry-inserts-one-layers-entry-for-a-child-name]

```sql
INSERT INTO path_entries
    (parent_guid, child_name, child_name_folded, layer,
     target_type, target_guid, sequence)
VALUES (?, ?, fold(?), ?, 0, ?, ?)
```

The target table follows the store containing the child key (§5.1).
[*entry.create-entry-targets-the-child-keys-store]
A child GUID in neither store lands in the persistent table.
[*entry.create-entry-for-an-unknown-child-guid-lands-in-the-persistent-store]

`RSI_ALREADY_EXISTS` comes from the target table's primary key on
`(parent_guid, child_name_folded, layer)`.
[*entry.create-entry-refuses-a-duplicate-parent-name-layer-triple]
That key binds one schema, so
loregd also asks the *other* store for the same triple before inserting,
and answers `RSI_ALREADY_EXISTS` if it finds one.
[*entry.create-entry-checks-both-stores-for-a-duplicate-triple]
Nothing de-duplicates on
read, so a triple in both stores would put two entries claiming the same
name in the same layer inside one child block (§5.1).

An unresolvable parent GUID returns `RSI_NOT_FOUND`.
[*entry.create-entry-on-an-unresolvable-parent-returns-not-found]

## RSI_HIDE_ENTRY [*entry.hide-entry-writes-a-tombstone-masking-lower-layers]

Writes a tombstone that masks the same name in lower layers:

```sql
INSERT OR REPLACE INTO path_entries
    (parent_guid, child_name, child_name_folded, layer,
     target_type, target_guid, sequence)
VALUES (?, ?, fold(?), ?, 1, NULL, ?)
```

`target_type` is 1 and `target_guid` is null.
[*entry.a-hide-entry-tombstone-has-type-one-and-a-null-target]
The target table follows the store containing the **parent** key, because a
volatile parent's entire subtree is volatile.
[*entry.hide-entry-targets-the-parent-keys-store]
A parent GUID in neither store returns `RSI_NOT_FOUND`.
[*entry.hide-entry-on-an-unresolvable-parent-returns-not-found]

## RSI_DELETE_ENTRY [*entry.delete-entry-removes-one-layers-entry-from-both-stores]

Removes one layer's entry for one name, from both stores:

```sql
DELETE FROM path_entries
WHERE parent_guid = ? AND child_name_folded = ? AND layer = ?
```

No rows-affected check is made, so deleting an entry that is not there
succeeds. [*entry.deleting-an-absent-entry-succeeds]
A parent GUID that resolves to no hive returns
`RSI_NOT_FOUND` rather than succeeding.
[*entry.delete-entry-on-an-unresolvable-parent-returns-not-found]

## RSI_ENUM_CHILDREN [*entry.enum-children-returns-every-layers-entry-for-every-child]

Returns every layer's entry for every child under a parent:

```sql
SELECT child_name, child_name_folded, layer, target_type,
       target_guid, sequence
FROM main.path_entries WHERE parent_guid = ?
UNION ALL
SELECT child_name, child_name_folded, layer, target_type,
       target_guid, sequence
FROM volatile.path_entries WHERE parent_guid = ?
```

Rows are grouped by folded child name into one block per child, each
carrying that child's per-layer entries.
[*entry.enum-children-groups-rows-into-one-block-per-folded-child-name]
Metadata for the distinct
non-HIDDEN target GUIDs is fetched and emitted exactly as for
`RSI_LOOKUP`, including its treatment of an entry whose key record does not
yet exist: the entry is dropped, and a child left holding no entries at all
is dropped with it, so the child reads as absent rather than failing the
enumeration. [*entry.enum-children-drops-a-child-left-holding-no-entries]
The metadata block for a dropped entry is omitted with it —
the kernel rejects a GUID-typed entry carrying no metadata, and equally a
metadata block no entry references.
[*entry.enum-children-omits-the-metadata-block-of-a-dropped-entry]

Ordering, and the treatment of two rows whose folded names match but whose
stored case differs, are covered in §5.2.
