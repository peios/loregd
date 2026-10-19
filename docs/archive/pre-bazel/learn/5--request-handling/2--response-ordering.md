---
title: Response Ordering
description: The queries behind enumerations carry no ORDER BY, so their order is not stable across calls — and what that means for the kernel.
---

The queries backing enumerations and lookups carry no `ORDER BY`, and a
`UNION ALL` across the two stores yields rows in whatever order SQLite
produces them. That order is not stable across calls.
[*order.the-underlying-query-order-is-not-stable-across-calls]

The kernel walks enumeration results by dense index across repeated
calls, so an unstable order would make that walk duplicate or drop
entries. loregd therefore sorts every affected array into a canonical
order before encoding a response:
[*order.every-affected-array-is-sorted-into-a-canonical-order]

| Response | Sorted by |
|---|---|
| `RSI_LOOKUP` path entries | layer, then sequence [*order.lookup-path-entries-sort-by-layer-then-sequence] |
| `RSI_ENUM_CHILDREN` children | folded child name [*order.enum-children-sorts-children-by-folded-name] |
| `RSI_ENUM_CHILDREN` per-child entries | layer, then sequence [*order.enum-children-per-child-entries-sort-by-layer-then-sequence] |
| `RSI_QUERY_VALUES` value entries | folded value name, then layer, then sequence [*order.query-values-entries-sort-by-folded-name-then-layer-then-sequence] |
| `RSI_QUERY_VALUES` blanket tombstones | folded layer name, then sequence [*order.query-values-blanket-tombstones-sort-by-folded-layer-name-then-sequence] |
| `RSI_DELETE_LAYER` orphan GUIDs | ascending GUID, compared bytewise [*order.delete-layer-orphan-guids-sort-by-ascending-guid] |
| Key-metadata blocks, in any response | ascending GUID, compared bytewise [*order.key-metadata-blocks-sort-by-ascending-guid] |

This ordering is a wire-stability guarantee only. It has no bearing on
layer resolution, which is order-independent — the kernel selects a
maximum, not a first match.
[*order.the-ordering-is-a-wire-stability-guarantee-only]

## Arrays assembled outside one query [*order.two-arrays-are-assembled-first-and-sorted-afterwards]

Two of those arrays are not a single query's result set. They are
assembled first and sorted afterwards:

- The **blanket-tombstone array** in an `RSI_QUERY_VALUES` response comes
  from its own `UNION ALL`, separate from the value entries it travels
  beside, and takes the same order they do.
  [*order.the-blanket-tombstone-array-takes-the-same-order-as-the-value-entries]
- The **orphan-GUID array** in an `RSI_DELETE_LAYER` response is the
  concatenation of every registered hive's orphan set. That walk ranges a
  Go map, whose iteration order is randomised, so the array is sorted
  bytewise before it is encoded.
  [*order.the-orphan-guid-array-is-sorted-bytewise-before-it-is-encoded]

## Child display names

`RSI_ENUM_CHILDREN` groups rows by `child_name_folded` and emits one
child block per folded name, carrying a display name taken from the
`child_name` column.
[*order.enum-children-emits-one-child-block-per-folded-name]

Where two rows share a folded name but differ in stored case — `Foo` in
one store and `FOO` in the other, say — the display name emitted is the
lower of the two bytewise, which is a property of the set rather than of
the order the union produced.
[*order.the-display-name-is-the-bytewise-lower-of-the-stored-cases] Both
the *order* of children and the *case* of the name reported for each are
therefore stable across calls.
[*order.child-order-and-reported-case-are-both-stable-across-calls]
