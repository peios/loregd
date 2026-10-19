package handler

import (
	"database/sql"
	"log"
	"strings"
	"time"

	"github.com/peios/loregd/internal/fold"
	"github.com/peios/loregd/internal/rsi"
)

// --- Key operations ---

func (h *Handler) handleCreateKey(header rsi.RequestHeader, payload []byte) (status uint32, response []byte) {
	hdr, err := h.startRequest(header)
	if err != nil {
		return mapWriteErr("request", err), nil
	}
	defer h.finishRequest(hdr, &status)
	req, err := rsi.DecodeCreateKeyRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive, err := h.resolveHive(hdr, req.ParentGUID)
	if err != nil {
		log.Printf("create entry: %v", err)
		return storageStatus(err), nil
	}
	if hive == nil {
		for _, hv := range h.hives {
			if rsi.GUID(hv.RootGUID) == req.ParentGUID {
				hive = hv
				break
			}
		}
		if hive == nil {
			return rsi.StatusNotFound, nil
		}
	}

	wq, err := h.writeQ(hdr, hive)
	if err != nil {
		return mapWriteErr("create key", err), nil
	}

	now := time.Now().UnixNano()
	foldedName := fold.String(req.Name)

	table := "main.keys"
	other := "volatile"
	if req.Volatile {
		table = "volatile.keys"
		other = "main"
	}

	// The target table's PK covers only its own schema, so ask the other one
	// too. Without this a create whose GUID already exists in the opposite
	// store returned RSI_OK and left the GUID in both.
	if exists, err := keyExistsInQ(wq, other, req.GUID); err != nil {
		log.Printf("create key cross-schema check: %v", err)
		return storageStatus(err), nil
	} else if exists {
		return rsi.StatusAlreadyExists, nil
	}

	volInt := 0
	if req.Volatile {
		volInt = 1
	}
	symInt := 0
	if req.Symlink {
		symInt = 1
	}

	_, err = wq.Exec(`
		INSERT INTO `+table+` (guid, name, name_folded, parent_guid, sd, volatile, symlink, last_write_time)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, req.GUID[:], req.Name, foldedName, req.ParentGUID[:], req.SD, volInt, symInt, now)
	if err != nil {
		if isUniqueViolation(err) {
			return rsi.StatusAlreadyExists, nil
		}
		log.Printf("create key error: %v", err)
		return storageStatus(err), nil
	}

	// Cache immediately; needed for resolveHive within the same
	// transaction (the read pool can't see uncommitted data).
	h.guidCache.Store(req.GUID, hive)
	if hdr.TxnID != 0 {
		// On abort, remove the uncommitted GUID from cache.
		guid := req.GUID
		h.txns.addAbortHook(hdr.TxnID, func() { h.guidCache.Delete(guid) })
	}
	return rsi.StatusOK, nil
}

func (h *Handler) handleReadKey(header rsi.RequestHeader, payload []byte) (status uint32, response []byte) {
	hdr, err := h.startRequest(header)
	if err != nil {
		return mapWriteErr("request", err), nil
	}
	defer h.finishRequest(hdr, &status)
	req, err := rsi.DecodeReadKeyRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive, st, ok := h.resolveHiveStatus(hdr, "resolveHive", req.GUID)
	if !ok {
		return st, nil
	}

	db, err := h.readQ(hdr, hive)
	if err != nil {
		return mapReadErr("read key", err), nil
	}
	m, err := readKeyFull(db, req.GUID)
	if err != nil {
		if err == sql.ErrNoRows {
			return rsi.StatusNotFound, nil
		}
		log.Printf("read key error: %v", err)
		return storageStatus(err), nil
	}

	enc := rsi.NewEncoder(128)
	rsi.EncodeReadKeyResponse(enc, m)
	return rsi.StatusOK, enc.Bytes()
}

func (h *Handler) handleWriteKey(header rsi.RequestHeader, payload []byte) (status uint32, response []byte) {
	hdr, err := h.startRequest(header)
	if err != nil {
		return mapWriteErr("request", err), nil
	}
	defer h.finishRequest(hdr, &status)
	req, err := rsi.DecodeWriteKeyRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	if req.FieldMask & ^uint32(rsi.WriteKeyFieldSD|rsi.WriteKeyFieldLastWriteTime) != 0 {
		return rsi.StatusInvalid, nil
	}

	hive, st, ok := h.resolveHiveStatus(hdr, "resolveHive", req.GUID)
	if !ok {
		return st, nil
	}

	wq, err := h.writeQ(hdr, hive)
	if err != nil {
		return mapWriteErr("write key", err), nil
	}

	table := "main.keys"
	vol, volErr := isVolatileQ(wq, req.GUID)
	if volErr == errKeyNotFound {
		return rsi.StatusNotFound, nil
	}
	if volErr != nil {
		log.Printf("write key isVolatile: %v", volErr)
		return storageStatus(volErr), nil
	}
	if vol {
		table = "volatile.keys"
	}

	var result sql.Result
	if req.FieldMask&rsi.WriteKeyFieldSD != 0 && req.FieldMask&rsi.WriteKeyFieldLastWriteTime != 0 {
		result, err = wq.Exec(
			`UPDATE `+table+` SET sd = ?, last_write_time = ? WHERE guid = ?`,
			req.SD, req.LastWriteTime, req.GUID[:],
		)
	} else if req.FieldMask&rsi.WriteKeyFieldSD != 0 {
		result, err = wq.Exec(`UPDATE `+table+` SET sd = ? WHERE guid = ?`, req.SD, req.GUID[:])
	} else if req.FieldMask&rsi.WriteKeyFieldLastWriteTime != 0 {
		result, err = wq.Exec(`UPDATE `+table+` SET last_write_time = ? WHERE guid = ?`, req.LastWriteTime, req.GUID[:])
	} else {
		// field_mask == 0: no-op, but verify the key exists.
		var exists int
		err = wq.QueryRow(`SELECT 1 FROM `+table+` WHERE guid = ? LIMIT 1`, req.GUID[:]).Scan(&exists)
		if err == sql.ErrNoRows {
			return rsi.StatusNotFound, nil
		}
		if err != nil {
			log.Printf("write key existence check: %v", err)
			return storageStatus(err), nil
		}
		return rsi.StatusOK, nil
	}

	if err != nil {
		log.Printf("write key error: %v", err)
		return storageStatus(err), nil
	}

	// Check that the UPDATE matched a row.
	// modernc's SQLite Result stores this count directly and cannot fail here.
	if n, _ := result.RowsAffected(); n == 0 {
		return rsi.StatusNotFound, nil
	}

	return rsi.StatusOK, nil
}

func (h *Handler) handleDropKey(header rsi.RequestHeader, payload []byte) (status uint32, response []byte) {
	hdr, err := h.startRequest(header)
	if err != nil {
		return mapWriteErr("request", err), nil
	}
	defer h.finishRequest(hdr, &status)
	req, err := rsi.DecodeDropKeyRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive, err := h.resolveHive(hdr, req.GUID)
	if err != nil {
		log.Printf("drop key: %v", err)
		return storageStatus(err), nil
	}
	if hive == nil {
		// Dropping a key that is already gone is a success. Dropping one
		// whose hive cannot be read is not, which is why the error is
		// checked first.
		return rsi.StatusOK, nil
	}

	wq, err := h.writeQ(hdr, hive)
	if err != nil {
		return mapWriteErr("drop key", err), nil
	}

	dropDeletes := []string{
		`DELETE FROM main.keys WHERE guid = ?`,
		`DELETE FROM main.path_entries WHERE target_guid = ?`,
		`DELETE FROM main.[values] WHERE key_guid = ?`,
		`DELETE FROM main.blanket_tombstones WHERE key_guid = ?`,
		`DELETE FROM volatile.keys WHERE guid = ?`,
		`DELETE FROM volatile.path_entries WHERE target_guid = ?`,
		`DELETE FROM volatile.[values] WHERE key_guid = ?`,
		`DELETE FROM volatile.blanket_tombstones WHERE key_guid = ?`,
	}

	// For non-transactional drops, wrap in a local transaction.
	// For transactional drops, the outer transaction provides atomicity.
	if hdr.TxnID == 0 {
		tx, err := hive.WriteDB().BeginTx(hdr.ctx, nil)
		if err != nil {
			log.Printf("drop key begin tx: %v", err)
			return storageStatus(err), nil
		}
		defer tx.Rollback()
		for _, q := range dropDeletes {
			if _, err := tx.Exec(q, req.GUID[:]); err != nil {
				log.Printf("drop key: %v", err)
				return storageStatus(err), nil
			}
		}
		if err := tx.Commit(); err != nil {
			log.Printf("drop key commit: %v", err)
			return storageStatus(err), nil
		}
	} else {
		for _, q := range dropDeletes {
			if _, err := wq.Exec(q, req.GUID[:]); err != nil {
				log.Printf("drop key (txn): %v", err)
				return storageStatus(err), nil
			}
		}
	}

	if hdr.TxnID != 0 {
		// Defer cache eviction to commit; the key still exists until then.
		guid := req.GUID
		h.txns.addCommitHook(hdr.TxnID, func() { h.guidCache.Delete(guid) })
	} else {
		h.guidCache.Delete(req.GUID)
	}
	return rsi.StatusOK, nil
}

// --- Helpers ---

func readKeyMeta(db Querier, guid rsi.GUID) (rsi.LookupKeyMeta, error) {
	var m rsi.LookupKeyMeta
	m.GUID = guid
	var vol, sym int
	err := db.QueryRow(`
		SELECT sd, volatile, symlink, last_write_time FROM main.keys WHERE guid = ?
		UNION ALL
		SELECT sd, volatile, symlink, last_write_time FROM volatile.keys WHERE guid = ?
		LIMIT 1
	`, guid[:], guid[:]).Scan(&m.SD, &vol, &sym, &m.LastWriteTime)
	if err != nil {
		return m, err
	}
	m.Volatile = vol != 0
	m.Symlink = sym != 0
	return m, nil
}

func readKeyFull(db Querier, guid rsi.GUID) (rsi.ReadKeyResponse, error) {
	var r rsi.ReadKeyResponse
	var parentGUID []byte
	var vol, sym int
	err := db.QueryRow(`
		SELECT name, parent_guid, sd, volatile, symlink, last_write_time FROM main.keys WHERE guid = ?
		UNION ALL
		SELECT name, parent_guid, sd, volatile, symlink, last_write_time FROM volatile.keys WHERE guid = ?
		LIMIT 1
	`, guid[:], guid[:]).Scan(&r.Name, &parentGUID, &r.SD, &vol, &sym, &r.LastWriteTime)
	if err != nil {
		return r, err
	}
	if len(parentGUID) == 16 {
		copy(r.ParentGUID[:], parentGUID)
	}
	r.Volatile = vol != 0
	r.Symlink = sym != 0
	return r, nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
