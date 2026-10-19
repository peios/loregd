package handler

import (
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/peios/loregd/internal/hivedb"
	"github.com/peios/loregd/internal/rsi"
)

// --- Querier routing ---

// writeQ returns the appropriate write querier for a request.
// For transactional requests, returns the pinned transaction connection
// (binding the write connection on the first mutating op). For
// non-transactional requests, returns the hive's write DB. Returns
// errReadOnlyTxn if the request is tagged with a read-only transaction.
func (h *Handler) writeQ(hdr *request, hive *hivedb.HiveDB) (Querier, error) {
	if err := h.txns.mutationAllowed(hdr.TxnID); err != nil {
		return nil, err
	}
	if err := hdr.lockHive(hive, true); err != nil {
		return nil, err
	}
	if h.txns.otherWriter(hive, hdr.TxnID) {
		return nil, errWriterBusy
	}
	if hdr.TxnID != 0 {
		q, err := h.txns.getOrBindWrite(hdr.ctx, hdr.TxnID, hive)
		if err != nil {
			return nil, err
		}
		return q, nil
	}
	return &dbQuerier{db: hive.WriteDB(), ctx: hdr.ctx}, nil
}

// readQ returns the appropriate read querier for a request, plus an error
// if binding a read-only snapshot failed. For a bound read-write
// transaction it returns the pinned connection (read-your-own-writes); for
// a read-only transaction it returns a pinned snapshot connection; otherwise
// it returns a connection from the read pool.
func (h *Handler) readQ(hdr *request, hive *hivedb.HiveDB) (Querier, error) {
	if err := hdr.lockHive(hive, false); err != nil {
		return nil, err
	}
	if hdr.TxnID != 0 {
		q, bound, err := h.txns.getReadQuerier(hdr.ctx, hdr.TxnID, hive)
		if err != nil {
			return nil, err
		}
		if bound {
			return q, nil
		}
	}
	return &dbQuerier{db: hive.ReadDB(), ctx: hdr.ctx}, nil
}

// mapWriteErr classifies a writeQ failure into an RSI status code.
func mapWriteErr(op string, err error) uint32 {
	switch {
	case errors.Is(err, errReadOnlyTxn):
		return rsi.StatusInvalid
	case isBusy(err):
		return rsi.StatusTxnBusy
	default:
		log.Printf("%s writeQ: %v", op, err)
		return rsi.StatusStorageError
	}
}

// mapReadErr classifies a readQ failure into an RSI status code.
func mapReadErr(op string, err error) uint32 {
	if isBusy(err) {
		return rsi.StatusTxnBusy
	}
	log.Printf("%s readQ: %v", op, err)
	return rsi.StatusStorageError
}

// --- Hive resolution ---

// resolveHive maps a key GUID to the hive that holds it. A nil hive with
// a nil error means no hive holds the GUID; a non-nil error means at
// least one hive could not be read, and the caller must report a storage
// failure rather than a negative answer.
//
// Only sql.ErrNoRows advances to the next hive. Treating every error as
// "not in this one" reported an unreadable database to LCS as "this key
// does not exist" — the worst available mapping, because the kernel
// cannot then distinguish a key that was deleted from one it can no
// longer read, and a caller sees a clean negative rather than something
// to retry or escalate.
func (h *Handler) resolveHive(hdr *request, guid rsi.GUID) (*hivedb.HiveDB, error) {
	if v, ok := h.guidCache.Load(guid); ok {
		return v.(*hivedb.HiveDB), nil
	}
	for _, hive := range h.hives {
		var exists int
		unlock, err := hive.Gate.Acquire(hdr.ctx, false)
		if err != nil {
			return nil, err
		}
		var q Querier = &dbQuerier{db: hive.ReadDB(), ctx: hdr.ctx}
		if bound := h.txns.boundQuerier(hdr.TxnID, hive, hdr.ctx); bound != nil {
			q = bound
		}
		err = q.QueryRow(
			"SELECT 1 FROM main.keys WHERE guid = ? UNION ALL SELECT 1 FROM volatile.keys WHERE guid = ? LIMIT 1",
			guid[:], guid[:],
		).Scan(&exists)
		unlock()
		switch {
		case err == nil:
			h.guidCache.Store(guid, hive)
			return hive, nil
		case errors.Is(err, sql.ErrNoRows):
			continue // genuinely absent from this hive
		default:
			return nil, fmt.Errorf("probing hive %s for %x: %w", hive.Name, guid, err)
		}
	}
	return nil, nil
}

// resolveHiveStatus is the caller-side shape of resolveHive: it returns
// the hive, or the RSI status to answer with and false.
func (h *Handler) resolveHiveStatus(hdr *request, op string, guid rsi.GUID) (*hivedb.HiveDB, uint32, bool) {
	hive, err := h.resolveHive(hdr, guid)
	if err != nil {
		log.Printf("%s: %v", op, err)
		return nil, storageStatus(err), false
	}
	if hive == nil {
		return nil, rsi.StatusNotFound, false
	}
	return hive, 0, true
}

// errKeyNotFound is returned by isVolatileQ when the GUID is not in either database.
var errKeyNotFound = fmt.Errorf("key GUID not found")

// isVolatileQ reports which schema contains a key GUID, checking main first.
// The redundant volatile column does not control routing if it disagrees with
// the row's actual store. Returns errKeyNotFound for absence from both schemas,
// or a wrapped error for real storage errors.
func isVolatileQ(q Querier, guid rsi.GUID) (bool, error) {
	var one int
	err := q.QueryRow("SELECT 1 FROM main.keys WHERE guid = ?", guid[:]).Scan(&one)
	if err == nil {
		return false, nil
	}
	if err != sql.ErrNoRows {
		return false, fmt.Errorf("isVolatileQ main: %w", err)
	}
	// Not in main; check volatile.
	err = q.QueryRow("SELECT 1 FROM volatile.keys WHERE guid = ?", guid[:]).Scan(&one)
	if err == nil {
		return true, nil
	}
	if err != sql.ErrNoRows {
		return false, fmt.Errorf("isVolatileQ volatile: %w", err)
	}
	return false, errKeyNotFound
}

// keyExistsInQ reports whether a key GUID already exists in the named schema.
//
// A primary key constrains one schema only, and persistent and volatile data
// live in two schemas carrying identical tables. So the target table's PK --
// which is what every create relies on for RSI_ALREADY_EXISTS -- says nothing
// about the other store, and the same GUID could come to exist in both. Reads
// then resolve it arbitrarily to whichever row a UNION ALL puts first, leaving
// the loser unreachable while still occupying its GUID.
func keyExistsInQ(q Querier, schema string, guid rsi.GUID) (bool, error) {
	var one int
	err := q.QueryRow("SELECT 1 FROM "+schema+".keys WHERE guid = ?", guid[:]).Scan(&one)
	if err == nil {
		return true, nil
	}
	if err != sql.ErrNoRows {
		return false, fmt.Errorf("keyExistsInQ %s: %w", schema, err)
	}
	return false, nil
}

// pathEntryExistsInQ is the same check for a path entry's uniqueness triple.
func pathEntryExistsInQ(q Querier, schema string, parentGUID rsi.GUID, foldedName, layer string) (bool, error) {
	var one int
	err := q.QueryRow(
		"SELECT 1 FROM "+schema+".path_entries WHERE parent_guid = ? AND child_name_folded = ? AND layer = ?",
		parentGUID[:], foldedName, layer).Scan(&one)
	if err == nil {
		return true, nil
	}
	if err != sql.ErrNoRows {
		return false, fmt.Errorf("pathEntryExistsInQ %s: %w", schema, err)
	}
	return false, nil
}
