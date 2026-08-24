package handler

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"sort"

	"github.com/peios/loregd/internal/fold"
	"github.com/peios/loregd/internal/rsi"
)

func (h *Handler) registerValueHandlers(d *rsi.Dispatcher) {
	d.Register(rsi.OpQueryValues, h.handleQueryValues)
	d.Register(rsi.OpSetValue, h.handleSetValue)
	d.Register(rsi.OpDeleteValueEntry, h.handleDeleteValueEntry)
	d.Register(rsi.OpSetBlanketTombstone, h.handleSetBlanketTombstone)
}

func (h *Handler) handleQueryValues(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeQueryValuesRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive, st, ok := h.resolveHiveStatus("resolveHive", req.GUID)
	if !ok {
		return st, nil
	}

	db, err := h.readQ(hdr, hive)
	if err != nil {
		return mapReadErr("query values", err), nil
	}

	entries, err := queryValueEntries(db, req)
	if err != nil {
		log.Printf("query values error: %v", err)
		return rsi.StatusStorageError, nil
	}

	blankets, err := queryBlanketTombstones(db, req.GUID)
	if err != nil {
		log.Printf("query blanket tombstones error: %v", err)
		return rsi.StatusStorageError, nil
	}

	enc := rsi.NewEncoder(256)
	rsi.EncodeQueryValuesResponse(enc, entries, blankets)
	return rsi.StatusOK, enc.Bytes()
}

func queryValueEntries(db Querier, req rsi.QueryValuesRequest) ([]rsi.QueryValuesEntry, error) {
	var rows *sql.Rows
	var err error
	if req.QueryAll {
		rows, err = db.Query(`
			SELECT name, layer, type, data, sequence
			FROM main.[values] WHERE key_guid = ?
			UNION ALL
			SELECT name, layer, type, data, sequence
			FROM volatile.[values] WHERE key_guid = ?
		`, req.GUID[:], req.GUID[:])
	} else {
		foldedName := fold.String(req.ValueName)
		rows, err = db.Query(`
			SELECT name, layer, type, data, sequence
			FROM main.[values]
			WHERE key_guid = ? AND name_folded = ?
			UNION ALL
			SELECT name, layer, type, data, sequence
			FROM volatile.[values]
			WHERE key_guid = ? AND name_folded = ?
		`, req.GUID[:], foldedName, req.GUID[:], foldedName)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []rsi.QueryValuesEntry
	for rows.Next() {
		var e rsi.QueryValuesEntry
		if err := rows.Scan(&e.ValueName, &e.LayerName, &e.Type, &e.Data, &e.Sequence); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Deterministic order (folded value name, then layer, then sequence): the
	// LCS walks values by dense index across repeated RSI calls, so an unstable
	// order would dup/skip values exactly as an unordered child set does. The
	// UNION ALL above is unordered, so canonicalise here (PSD-006 §5).
	sort.SliceStable(entries, func(i, j int) bool {
		fi, fj := fold.String(entries[i].ValueName), fold.String(entries[j].ValueName)
		if fi != fj {
			return fi < fj
		}
		if entries[i].LayerName != entries[j].LayerName {
			return entries[i].LayerName < entries[j].LayerName
		}
		return entries[i].Sequence < entries[j].Sequence
	})
	return entries, nil
}

func queryBlanketTombstones(db Querier, guid rsi.GUID) ([]rsi.BlanketTombstoneEntry, error) {
	rows, err := db.Query(`
		SELECT layer, sequence
		FROM main.blanket_tombstones WHERE key_guid = ?
		UNION ALL
		SELECT layer, sequence
		FROM volatile.blanket_tombstones WHERE key_guid = ?
	`, guid[:], guid[:])
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var blankets []rsi.BlanketTombstoneEntry
	for rows.Next() {
		var bt rsi.BlanketTombstoneEntry
		if err := rows.Scan(&bt.LayerName, &bt.Sequence); err != nil {
			return nil, err
		}
		blankets = append(blankets, bt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The blanket list is the one enumeration block that was left
	// uncanonicalised. It comes from the same unordered UNION ALL as the
	// value entries and travels in the same message, so it gets the same
	// (folded layer, sequence) order.
	//
	// Resolution itself is order-independent — it selects by (precedence,
	// sequence) — so this is not a live correctness failure. It matters
	// because the deterministic-enumeration obligation is either met or
	// not, and because a blanket list with a duplicate winning
	// (precedence, sequence) is rejected by the kernel as malformed
	// source data: stable ordering makes that failure reproducible rather
	// than intermittent.
	sort.SliceStable(blankets, func(i, j int) bool {
		fi, fj := fold.String(blankets[i].LayerName), fold.String(blankets[j].LayerName)
		if fi != fj {
			return fi < fj
		}
		return blankets[i].Sequence < blankets[j].Sequence
	})
	return blankets, nil
}

func (h *Handler) handleSetValue(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeSetValueRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive, st, ok := h.resolveHiveStatus("resolveHive", req.GUID)
	if !ok {
		return st, nil
	}

	wq, err := h.writeQ(hdr, hive)
	if err != nil {
		return mapWriteErr("set value", err), nil
	}

	foldedName := fold.String(req.ValueName)

	table := "main.[values]"
	vol, volErr := isVolatileQ(wq, req.GUID)
	if volErr == errKeyNotFound {
		return rsi.StatusNotFound, nil
	}
	if volErr != nil {
		log.Printf("set value isVolatile: %v", volErr)
		return rsi.StatusStorageError, nil
	}
	if vol {
		table = "volatile.[values]"
	}

	// Conditional write (CAS).
	if req.ExpectedSequence != 0 {
		// For non-transactional CAS, wrap in a local transaction.
		// For transactional CAS, the outer transaction provides atomicity.
		if hdr.TxnID == 0 {
			// Use BEGIN IMMEDIATE for CAS atomicity; a deferred
			// BEGIN would allow concurrent writers between SELECT and INSERT.
			ctx := context.Background()
			conn, connErr := hive.WriteDB().Conn(ctx)
			if connErr != nil {
				log.Printf("set value CAS conn: %v", connErr)
				return rsi.StatusStorageError, nil
			}
			defer conn.Close()

			_, connErr = conn.ExecContext(ctx, "BEGIN IMMEDIATE")
			if connErr != nil {
				if isBusy(connErr) {
					return rsi.StatusTxnBusy, nil
				}
				log.Printf("set value CAS begin: %v", connErr)
				return rsi.StatusStorageError, nil
			}

			var currentSeq uint64
			err = conn.QueryRowContext(ctx, `SELECT sequence FROM `+table+` WHERE key_guid = ? AND name_folded = ? AND layer = ?`,
				req.GUID[:], foldedName, req.LayerName).Scan(&currentSeq)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && currentSeq != req.ExpectedSequence) {
				conn.ExecContext(ctx, "ROLLBACK")
				return rsi.StatusCASFailed, nil
			}
			if err != nil {
				conn.ExecContext(ctx, "ROLLBACK")
				log.Printf("set value CAS check: %v", err)
				return rsi.StatusStorageError, nil
			}

			_, err = conn.ExecContext(ctx, `INSERT OR REPLACE INTO `+table+` (key_guid, name, name_folded, layer, type, data, sequence) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				req.GUID[:], req.ValueName, foldedName, req.LayerName, req.Type, req.Data, req.Sequence)
			if err != nil {
				conn.ExecContext(ctx, "ROLLBACK")
				log.Printf("set value CAS write: %v", err)
				return rsi.StatusStorageError, nil
			}
			_, err = conn.ExecContext(ctx, "COMMIT")
			if err != nil {
				log.Printf("set value CAS commit: %v", err)
				return rsi.StatusStorageError, nil
			}
		} else {
			// Within RSI transaction; already on the pinned connection.
			var currentSeq uint64
			err = wq.QueryRow(`SELECT sequence FROM `+table+` WHERE key_guid = ? AND name_folded = ? AND layer = ?`,
				req.GUID[:], foldedName, req.LayerName).Scan(&currentSeq)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && currentSeq != req.ExpectedSequence) {
				return rsi.StatusCASFailed, nil
			}
			if err != nil {
				log.Printf("set value CAS check: %v", err)
				return rsi.StatusStorageError, nil
			}

			_, err = wq.Exec(`INSERT OR REPLACE INTO `+table+` (key_guid, name, name_folded, layer, type, data, sequence) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				req.GUID[:], req.ValueName, foldedName, req.LayerName, req.Type, req.Data, req.Sequence)
			if err != nil {
				log.Printf("set value CAS write: %v", err)
				return rsi.StatusStorageError, nil
			}
		}
		return rsi.StatusOK, nil
	}

	// Unconditional write.
	_, err = wq.Exec(`INSERT OR REPLACE INTO `+table+` (key_guid, name, name_folded, layer, type, data, sequence) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		req.GUID[:], req.ValueName, foldedName, req.LayerName, req.Type, req.Data, req.Sequence)
	if err != nil {
		log.Printf("set value error: %v", err)
		return rsi.StatusStorageError, nil
	}

	return rsi.StatusOK, nil
}

func (h *Handler) handleDeleteValueEntry(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeDeleteValueEntryRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive, st, ok := h.resolveHiveStatus("resolveHive", req.GUID)
	if !ok {
		return st, nil
	}

	wq, err := h.writeQ(hdr, hive)
	if err != nil {
		return mapWriteErr("delete value", err), nil
	}

	foldedName := fold.String(req.ValueName)

	if _, err := wq.Exec(`DELETE FROM main.[values] WHERE key_guid = ? AND name_folded = ? AND layer = ?`,
		req.GUID[:], foldedName, req.LayerName); err != nil {
		log.Printf("delete value entry (main): %v", err)
		return rsi.StatusStorageError, nil
	}
	if _, err := wq.Exec(`DELETE FROM volatile.[values] WHERE key_guid = ? AND name_folded = ? AND layer = ?`,
		req.GUID[:], foldedName, req.LayerName); err != nil {
		log.Printf("delete value entry (volatile): %v", err)
		return rsi.StatusStorageError, nil
	}

	return rsi.StatusOK, nil
}

func (h *Handler) handleSetBlanketTombstone(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeSetBlanketTombstoneRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive, st, ok := h.resolveHiveStatus("resolveHive", req.GUID)
	if !ok {
		return st, nil
	}

	wq, err := h.writeQ(hdr, hive)
	if err != nil {
		return mapWriteErr("blanket tombstone", err), nil
	}

	table := "main.blanket_tombstones"
	vol, volErr := isVolatileQ(wq, req.GUID)
	if volErr == errKeyNotFound {
		return rsi.StatusNotFound, nil
	}
	if volErr != nil {
		log.Printf("blanket tombstone isVolatile: %v", volErr)
		return rsi.StatusStorageError, nil
	}
	if vol {
		table = "volatile.blanket_tombstones"
	}

	if req.Set {
		_, err = wq.Exec(`INSERT OR REPLACE INTO `+table+` (key_guid, layer, sequence) VALUES (?, ?, ?)`,
			req.GUID[:], req.LayerName, req.Sequence)
	} else {
		_, err = wq.Exec(`DELETE FROM `+table+` WHERE key_guid = ? AND layer = ?`,
			req.GUID[:], req.LayerName)
	}

	if err != nil {
		log.Printf("blanket tombstone error: %v", err)
		return rsi.StatusStorageError, nil
	}

	return rsi.StatusOK, nil
}
