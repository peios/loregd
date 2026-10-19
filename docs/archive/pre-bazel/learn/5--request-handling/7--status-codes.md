---
title: Status Codes
description: The seven RSI status codes loregd returns, and the three the interface defines that it never produces.
---

loregd returns seven of the RSI status codes:
[*status.loregd-returns-seven-of-the-rsi-status-codes]

| Status | Value | Returned when |
|---|---|---|
| `RSI_OK` | 0 | The operation succeeded. Also returned by the idempotent deletions when their target was already absent. [*status.ok-is-returned-on-success-and-by-an-idempotent-deletion] |
| `RSI_NOT_FOUND` | 1 | A named key GUID exists in neither store, or resolves to no registered hive. Also an update matching no row. [*status.not-found-is-returned-for-an-unknown-guid-or-unresolvable-hive] |
| `RSI_ALREADY_EXISTS` | 2 | A duplicate key GUID or path entry: the target table's primary key, or the matching check against the other store (§5.3, §5.4). [*status.already-exists-is-returned-for-a-duplicate-key-guid-or-path-entry] No value operation produces it — every value write is an `INSERT OR REPLACE`. [*status.no-value-operation-returns-already-exists] |
| `RSI_STORAGE_ERROR` | 3 | A SQLite failure while serving the request. Also an operation whose GUID belongs to a hive other than the one its transaction is bound to, a mutating operation carrying an unknown transaction id, and a failed commit that was not busy. [*status.storage-error-is-returned-for-a-sqlite-failure-or-a-transaction-mismatch] |
| `RSI_TXN_BUSY` | 6 | `BEGIN IMMEDIATE` found the database busy, a conditional write could not begin its transaction, or `RSI_FLUSH` or `RSI_DELETE_LAYER` found a transaction already bound to a hive's write connection. [*status.txn-busy-is-returned-when-a-write-cannot-take-the-database] |
| `RSI_INVALID` | 7 | An unknown opcode, a request whose payload cannot be decoded, an out-of-range `RSI_WRITE_KEY` field mask, a mutating operation carrying a read-only transaction id, an `RSI_FLUSH` naming an unregistered hive, or an `RSI_BEGIN_TRANSACTION` re-using an active id or naming a mode other than `RSI_TXN_READ_WRITE` or `RSI_TXN_READ_ONLY`. [*status.invalid-is-returned-for-a-malformed-or-unacceptable-request] |
| `RSI_CAS_FAILED` | 8 | A conditional `RSI_SET_VALUE` whose target was absent or whose sequence did not match. [*status.cas-failed-is-returned-when-a-conditional-set-value-does-not-match] |

Three codes are defined by the interface and never produced by loregd:
`RSI_NOT_EMPTY` (4), `RSI_TOO_LARGE` (5), and `RSI_TXN_NOT_SUPPORTED` (9).
[*status.three-interface-codes-are-never-produced-by-loregd]

`RSI_TXN_NOT_SUPPORTED` is never needed because loregd supports both
transaction modes (§4.3).
[*status.txn-not-supported-is-never-returned]

`RSI_TOO_LARGE` is never returned because an oversized or malformed frame
is not answered at all.
[*status.too-large-is-never-returned-because-a-bad-frame-ends-the-connection]
Framing is validated before an opcode is known, and
a frame that fails validation ends the connection instead of producing a
response (§2.3).
