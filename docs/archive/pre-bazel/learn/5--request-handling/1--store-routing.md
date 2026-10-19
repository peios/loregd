---
title: Store Routing
description: Persistent data in the main schema and volatile data in the attached one — how an operation picks, and the ones that span both.
---

Persistent data lives in the hive database's `main` schema; volatile data
lives in the attached `volatile` schema (§3.3). Most operations act on one
of the two, and loregd has to decide which before it can run any SQL.

## Operations naming one key [*route.an-operation-naming-one-key-routes-on-the-keys-store]

For a write that names an existing key, the schema containing the key row
selects the store. loregd first probes `main.keys` by GUID, then probes
`volatile.keys` only if the first query found no row. A database error is
returned as a storage error, not treated as absence. If an externally modified
database contains the GUID in both stores, the existing main-first lookup
precedence applies; normal key creation rejects such duplicates.

A GUID present in neither produces `RSI_NOT_FOUND` for most operations.
[*route.a-guid-in-neither-store-produces-not-found]

Routing follows the **source table**, including when the stored `volatile`
column contradicts it. A row in `main.keys` carrying `volatile = 1` still
routes writes to `main`; a row in `volatile.keys` carrying 0 still routes
writes to `volatile`. An inconsistent flag must not redirect writes to the
other store. [*route.routing-follows-the-source-table-despite-contradictory-metadata]
Rows loregd creates carry the consistent flag for their store. This routing
rule does not repair external metadata changes: read-key and lookup metadata
continue to report the stored column value.

`RSI_CREATE_KEY` cannot consult a key that does not exist yet, so it
routes on the volatile flag carried in the request instead.
[*route.create-key-routes-on-the-requests-volatile-flag]

`RSI_CREATE_ENTRY` routes to the store containing the **child** key the
entry points at.
[*route.create-entry-routes-on-the-child-keys-store] When that
child GUID is present in neither store, the entry is written to the
persistent table.
[*route.an-entry-with-an-unknown-child-guid-goes-to-the-persistent-table]

`RSI_HIDE_ENTRY` routes on the **parent** key, since a HIDDEN entry
belongs to the parent's child list and a volatile parent's whole subtree
is volatile. [*route.hide-entry-routes-on-the-parent-key]

## Operations spanning both stores [*route.the-operations-that-span-both-stores]

`RSI_LOOKUP`, `RSI_ENUM_CHILDREN`, `RSI_READ_KEY` and `RSI_QUERY_VALUES`
are not scoped to one store: a persistent parent may have volatile
children, and a persistent key may have volatile-store rows beneath it.
Each issues a single `UNION ALL` statement over the two schemas rather
than querying them separately, so the merge happens inside SQLite.

Nothing de-duplicates across the two stores.
[*route.nothing-de-duplicates-across-the-two-stores] The primary keys that
make `(parent_guid, child_name_folded, layer)` unique apply *per schema*,
so if the same triple exists in both, both rows appear in the response.
[*route.a-triple-present-in-both-stores-appears-twice-in-the-response] The
same holds for value entries keyed on `(key_guid, name_folded, layer)`.
[*route.value-entries-are-not-de-duplicated-either]

The deletions — `RSI_DELETE_ENTRY`, `RSI_DELETE_VALUE_ENTRY` and
`RSI_DROP_KEY` — do not route at all. They delete from both schemas
unconditionally.
[*route.the-deletions-do-not-route-and-delete-from-both-schemas]
