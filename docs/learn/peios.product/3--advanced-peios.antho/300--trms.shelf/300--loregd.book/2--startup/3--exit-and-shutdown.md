---
title: Exit and Shutdown
description: What ends the loregd process, what pointedly does not, and what happens to the data in each case.
---

The daemon distinguishes process failures from errors returned for individual
requests.


## What ends the process

loregd exits when any of the following happens:

- **The kernel closes the registry device.** Reading from
  `/dev/pkm_registry` returns end-of-file, the request loop returns, and
  loregd shuts down cleanly with status 0.
[*exit.device-eof-shuts-down-cleanly-with-status-zero]
 This is the
  normal path when the registry subsystem goes away.
- **A termination signal arrives.** `SIGTERM` and `SIGINT` are trapped.
  The handler closes the device, which unblocks the read loop and
  produces the same clean shutdown as above.
[*exit.sigterm-and-sigint-close-the-device-for-a-clean-shutdown]
 This
  is how the service manager stops loregd.
- **A request cannot be framed.** If a message read from the device
  cannot be parsed as an RSI request, loregd treats it as unrecoverable
  and exits non-zero.
[*exit.an-unframeable-request-exits-non-zero]
- **Startup preparation fails.** Database, device, negotiation, or
  registration failures in §2.2 exit with status 1. Readiness warnings do
  not stop the daemon.
[*exit.a-startup-failure-is-fatal]

On shutdown, the signal watcher stops and joins after serving ends.
In-flight requests are drained before the process exits
(§4.2), then outstanding transactions are rolled back, every connection is
closed,
and the per-hive volatile directories are removed before the runtime lock
is released.
[*exit.in-flight-requests-are-drained-and-every-connection-closed-on-shutdown]

## What does not end the process
[*exit.a-storage-failure-does-not-end-the-process]

A storage failure during request handling does **not** terminate loregd.
Errors from SQLite while serving a request; including I/O errors; are
converted into a status response. Lock contention and unbound request
deadline failures produce `RSI_TXN_BUSY`; other storage failures produce
`RSI_STORAGE_ERROR` and the daemon carries on
serving.
[*exit.sqlite-errors-become-an-rsi-storage-error-response]
 There
is no corruption detector and no disk-full detector that takes the
process down; a database that has become unreadable will produce a
stream of storage errors rather than an exit.
[*exit.there-is-no-corruption-or-disk-full-detector]

A running, registered daemon can therefore return errors for every
operation on a damaged hive. Process liveness alone does not establish
registry health.

## What happens to the data

Persistent data is durable at the point each transaction commits;
SQLite finalizes any outstanding WAL state as the connections close.
<!-- lint-disable-next-line spelling -->
[*exit.committed-data-is-durable-and-wal-state-is-finalised-at-close]

Volatile data is never recovered after process exit. Clean shutdown removes
the tmpfs stores; startup removes crash residue and creates new stores
before registering the hives (§3.3).
[*exit.volatile-data-does-not-survive-the-process]

When the kernel observes the source disconnect, it marks every hive
loregd served as unavailable.
[*exit.the-kernel-marks-every-served-hive-unavailable-on-disconnect]
