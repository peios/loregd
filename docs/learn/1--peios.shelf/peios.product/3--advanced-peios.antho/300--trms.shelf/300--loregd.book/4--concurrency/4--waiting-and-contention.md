---
title: Waiting and Contention
description: Bounded request waits, nonblocking transaction admission, and independent WAL readers.
---

Request deadlines bound admission and statement waits; kernel ownership
governs transaction cleanup.


## Request deadlines

Each RSI operation uses the absolute `CLOCK_MONOTONIC` deadline supplied by
LCS through `REG_SRC_REQUEST_CONTEXT`. The budget includes kernel admission,
queue time, connection-pool waits, publication-gate waits, and SQL statements;
receiving the request does not restart it. A contended wait that expires
returns `RSI_TXN_BUSY`; successful operations retain their success status.
SQLite's busy timeout is zero because its native busy wait can outlast a
canceled Go context. Busy classification uses SQLite and Go error codes.
[*contend.request-waits-have-a-bounded-context]

The lifetime extension is negotiated before hive registration. loregd requires
version 1 and fails startup on a kernel without it. The 25-second local budget
is only a fallback for direct, unnegotiated test clients.
[*contend.request-deadlines-come-from-the-negotiated-kernel-context]

## A transaction holding the writer

A competing mutation returns `RSI_TXN_BUSY` immediately while a read-write
transaction owns the hive's write connection. This includes non-transactional
key drops, conditional value writes, flushes, and layer deletion. Admission
and binding are coordinated under the hive gate, so the check cannot race a
new binding. A read-only snapshot is not counted as a writer.
[*contend.competing-writes-decline-while-a-transaction-owns-the-writer]

Both stores use WAL with private caches. Ordinary reads see committed data
while a writer remains open; backup snapshots do not hold volatile table
locks that block writers. A short per-hive gate excludes reads from commit
publication and non-transactional mutations, not from an entire transaction's
lifetime. Different hives have separate gates.
[*contend.readers-and-backups-do-not-wait-for-open-write-transactions]

## Cancellation and cleanup

Operations on the same transaction are serialized, including commit and
abort. Rollback uses a fresh cleanup context, and an uncertain connection
is discarded rather than returned to the pool with a live transaction.
Every replacement physical connection receives its WAL, foreign-key and
volatile-attachment setup again.
[*contend.cancellation-does-not-return-live-transactions-to-the-pool]

SQLite can automatically roll back an interrupted write. Such a bound
transaction is marked failed and subsequent use returns a storage error;
LCS aborts it. It is never silently rebound or allowed to autocommit.
[*contend.interrupted-bound-transactions-cannot-silently-autocommit]

LCS owns transaction lifetime. A live transaction file object (including all
duplicated handles) or an in-kernel operation owns the shared kernel
transaction
record. A source can renew its five-second cleanup lease only while that owner
and source session remain valid. SQL activity neither creates ownership nor
extends a transaction's lifetime deadline. An internal operation can have no
separate lifetime limit; its live operation scope is still required.
[*contend.transaction-expiry-is-owned-by-lcs]

A separate control loop renews leases with three seconds or less remaining,
checks expiration every 100 milliseconds, and reclaims expired transactions.
Control ioctls consume no ordinary RSI request slots. Final owner release,
abort, timeout, or a lost source session prevents renewal even if an abort
message cannot be delivered. Expired transactions reject new operations;
late renewals cannot revive them. Cleanup serializes on the transaction's
operation gate and waits for an already-admitted commit to resolve. A request
timeout is not evidence that a commit failed. The request deadline gates
commit admission; once admitted, SQL COMMIT runs without caller cancellation
and its actual result is reported, including through normal late-response
recovery. Shutdown drains requests and cleanup before closing databases.
<!-- lint-disable-next-line line-length -->
[*contend.leases-reclaim-abandoned-state-without-reviving-expired-transactions]
