// Package handler implements RSI operation handlers for loregd.
//
// Each handler takes a parsed RSI request and executes the
// corresponding SQL operations against the appropriate hive database.
package handler

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/peios/loregd/internal/fold"
	"github.com/peios/loregd/internal/hivedb"
	"github.com/peios/loregd/internal/rsi"
)

// Handler routes RSI operations to the correct hive database.
type Handler struct {
	hives     map[string]*hivedb.HiveDB // folded hive name -> DB
	guidCache sync.Map                  // rsi.GUID -> *hivedb.HiveDB
	txns      *txnManager
}

// New creates a Handler with the given hive databases.
// Seeds the GUID-to-hive cache with each hive's root GUID.
func New(hives []*hivedb.HiveDB) *Handler {
	h := &Handler{
		hives: make(map[string]*hivedb.HiveDB, len(hives)),
		txns:  newTxnManager(),
	}
	for _, hive := range hives {
		h.hives[fold.String(hive.Name)] = hive
		h.guidCache.Store(rsi.GUID(hive.RootGUID), hive)
	}
	return h
}

// Register wires all operation handlers into the dispatcher.
func (h *Handler) Register(d *rsi.Dispatcher) {
	d.Register(rsi.OpLookup, h.handleLookup)
	d.Register(rsi.OpCreateEntry, h.handleCreateEntry)
	d.Register(rsi.OpHideEntry, h.handleHideEntry)
	d.Register(rsi.OpDeleteEntry, h.handleDeleteEntry)
	d.Register(rsi.OpEnumChildren, h.handleEnumChildren)
	d.Register(rsi.OpCreateKey, h.handleCreateKey)
	d.Register(rsi.OpReadKey, h.handleReadKey)
	d.Register(rsi.OpWriteKey, h.handleWriteKey)
	d.Register(rsi.OpDropKey, h.handleDropKey)

	h.registerValueHandlers(d)
	h.registerTxnHandlers(d)
}

// --- Querier routing ---

// writeQ returns the appropriate write querier for a request.
// For transactional requests, returns the pinned transaction connection
// (binding the write connection on the first mutating op). For
// non-transactional requests, returns the hive's write DB. Returns
// errReadOnlyTxn if the request is tagged with a read-only transaction.
func (h *Handler) writeQ(hdr rsi.RequestHeader, hive *hivedb.HiveDB) (Querier, error) {
	if hdr.TxnID != 0 {
		q, err := h.txns.getOrBindWrite(hdr.TxnID, hive)
		if err != nil {
			return nil, err
		}
		return q, nil
	}
	return hive.WriteDB(), nil
}

// readQ returns the appropriate read querier for a request, plus an error
// if binding a read-only snapshot failed. For a bound read-write
// transaction it returns the pinned connection (read-your-own-writes); for
// a read-only transaction it returns a pinned snapshot connection; otherwise
// it returns a connection from the read pool.
func (h *Handler) readQ(hdr rsi.RequestHeader, hive *hivedb.HiveDB) (Querier, error) {
	if hdr.TxnID != 0 {
		q, bound, err := h.txns.getReadQuerier(hdr.TxnID, hive)
		if err != nil {
			return nil, err
		}
		if bound {
			return q, nil
		}
	}
	return hive.ReadDB(), nil
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

func (h *Handler) resolveHive(guid rsi.GUID) *hivedb.HiveDB {
	if v, ok := h.guidCache.Load(guid); ok {
		return v.(*hivedb.HiveDB)
	}
	for _, hive := range h.hives {
		var exists int
		err := hive.ReadDB().QueryRow(
			"SELECT 1 FROM main.keys WHERE guid = ? UNION ALL SELECT 1 FROM volatile.keys WHERE guid = ? LIMIT 1",
			guid[:], guid[:],
		).Scan(&exists)
		if err == nil {
			h.guidCache.Store(guid, hive)
			return hive
		}
	}
	return nil
}

func (h *Handler) resolveHiveByParent(parentGUID rsi.GUID) *hivedb.HiveDB {
	return h.resolveHive(parentGUID)
}

// errKeyNotFound is returned by isVolatileQ when the GUID is not in either database.
var errKeyNotFound = fmt.Errorf("key GUID not found")

// isVolatileQ checks the volatile flag for a key GUID.
// Returns (volatile, error). Returns errKeyNotFound if the GUID is
// not in either database. Returns a wrapped error for real storage errors.
func isVolatileQ(q Querier, guid rsi.GUID) (bool, error) {
	var v int
	err := q.QueryRow("SELECT volatile FROM main.keys WHERE guid = ?", guid[:]).Scan(&v)
	if err == nil {
		return v != 0, nil
	}
	if err != sql.ErrNoRows {
		return false, fmt.Errorf("isVolatileQ main: %w", err)
	}
	// Not in main; check volatile.
	err = q.QueryRow("SELECT volatile FROM volatile.keys WHERE guid = ?", guid[:]).Scan(&v)
	if err == nil {
		return v != 0, nil
	}
	if err != sql.ErrNoRows {
		return false, fmt.Errorf("isVolatileQ volatile: %w", err)
	}
	return false, errKeyNotFound
}

// --- Path operations ---

func (h *Handler) handleLookup(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeLookupRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive := h.resolveHiveByParent(req.ParentGUID)
	if hive == nil {
		return rsi.StatusNotFound, nil
	}

	db, err := h.readQ(hdr, hive)
	if err != nil {
		return mapReadErr("lookup", err), nil
	}
	foldedName := fold.String(req.ChildName)

	rows, err := db.Query(`
		SELECT layer, target_type, target_guid, sequence
		FROM main.path_entries
		WHERE parent_guid = ? AND child_name_folded = ?
		UNION ALL
		SELECT layer, target_type, target_guid, sequence
		FROM volatile.path_entries
		WHERE parent_guid = ? AND child_name_folded = ?
	`, req.ParentGUID[:], foldedName, req.ParentGUID[:], foldedName)
	if err != nil {
		log.Printf("lookup query error: %v", err)
		return rsi.StatusStorageError, nil
	}
	defer rows.Close()

	var entries []rsi.LookupPathEntry
	guidSet := make(map[rsi.GUID]bool)

	for rows.Next() {
		var e rsi.LookupPathEntry
		var targetGUID []byte
		if err := rows.Scan(&e.LayerName, &e.TargetType, &targetGUID, &e.Sequence); err != nil {
			log.Printf("lookup scan error: %v", err)
			return rsi.StatusStorageError, nil
		}
		if e.TargetType == rsi.TargetGUID && targetGUID != nil {
			copy(e.TargetGUID[:], targetGUID)
			guidSet[e.TargetGUID] = true
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		log.Printf("lookup iteration error: %v", err)
		return rsi.StatusStorageError, nil
	}

	var meta []rsi.LookupKeyMeta
	for guid := range guidSet {
		m, err := readKeyMeta(db, guid)
		if err != nil {
			log.Printf("lookup meta error for %x: %v", guid, err)
			return rsi.StatusStorageError, nil
		}
		meta = append(meta, m)
	}

	enc := rsi.NewEncoder(256)
	rsi.EncodeLookupResponse(enc, entries, meta)
	return rsi.StatusOK, enc.Bytes()
}

func (h *Handler) handleCreateEntry(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeCreateEntryRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive := h.resolveHiveByParent(req.ParentGUID)
	if hive == nil {
		return rsi.StatusNotFound, nil
	}

	wq, err := h.writeQ(hdr, hive)
	if err != nil {
		return mapWriteErr("create entry", err), nil
	}

	foldedName := fold.String(req.ChildName)

	table := "main.path_entries"
	vol, volErr := isVolatileQ(wq, req.ChildGUID)
	if volErr == errKeyNotFound {
		// Slice-1 bring-up shortcut: LCS dispatches RSI_CREATE_ENTRY before
		// RSI_CREATE_KEY, so the child key record isn't stored yet, and
		// CREATE_ENTRY carries no volatile flag on the wire (PSD-005 §7.2) for
		// us to choose the table from. Default to the persistent table rather
		// than failing the create. Correct for persistent keys (the common
		// case); WRONG for volatile children -- the entry would land in the
		// persistent table and outlive its key across a reboot. Proper fix
		// (Codex): LCS creates the key before the entry, or add a volatile flag
		// to the CREATE_ENTRY wire request.
		vol = false
	} else if volErr != nil {
		log.Printf("create entry isVolatile: %v", volErr)
		return rsi.StatusStorageError, nil
	}
	if vol {
		table = "volatile.path_entries"
	}

	_, err = wq.Exec(`
		INSERT INTO `+table+` (parent_guid, child_name, child_name_folded, layer, target_type, target_guid, sequence)
		VALUES (?, ?, ?, ?, 0, ?, ?)
	`, req.ParentGUID[:], req.ChildName, foldedName, req.LayerName, req.ChildGUID[:], req.Sequence)
	if err != nil {
		if isUniqueViolation(err) {
			return rsi.StatusAlreadyExists, nil
		}
		log.Printf("create entry error: %v", err)
		return rsi.StatusStorageError, nil
	}

	return rsi.StatusOK, nil
}

func (h *Handler) handleHideEntry(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeHideEntryRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive := h.resolveHiveByParent(req.ParentGUID)
	if hive == nil {
		return rsi.StatusNotFound, nil
	}

	wq, err := h.writeQ(hdr, hive)
	if err != nil {
		return mapWriteErr("hide entry", err), nil
	}

	foldedName := fold.String(req.ChildName)

	table := "main.path_entries"
	vol, volErr := isVolatileQ(wq, req.ParentGUID)
	if volErr == errKeyNotFound {
		return rsi.StatusNotFound, nil
	}
	if volErr != nil {
		log.Printf("hide entry isVolatile: %v", volErr)
		return rsi.StatusStorageError, nil
	}
	if vol {
		table = "volatile.path_entries"
	}

	_, err = wq.Exec(`
		INSERT OR REPLACE INTO `+table+` (parent_guid, child_name, child_name_folded, layer, target_type, target_guid, sequence)
		VALUES (?, ?, ?, ?, 1, NULL, ?)
	`, req.ParentGUID[:], req.ChildName, foldedName, req.LayerName, req.Sequence)
	if err != nil {
		log.Printf("hide entry error: %v", err)
		return rsi.StatusStorageError, nil
	}

	return rsi.StatusOK, nil
}

func (h *Handler) handleDeleteEntry(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeDeleteEntryRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive := h.resolveHiveByParent(req.ParentGUID)
	if hive == nil {
		return rsi.StatusNotFound, nil
	}

	wq, err := h.writeQ(hdr, hive)
	if err != nil {
		return mapWriteErr("delete entry", err), nil
	}

	foldedName := fold.String(req.ChildName)

	if _, err := wq.Exec(`DELETE FROM main.path_entries WHERE parent_guid = ? AND child_name_folded = ? AND layer = ?`,
		req.ParentGUID[:], foldedName, req.LayerName); err != nil {
		log.Printf("delete entry (main): %v", err)
		return rsi.StatusStorageError, nil
	}
	if _, err := wq.Exec(`DELETE FROM volatile.path_entries WHERE parent_guid = ? AND child_name_folded = ? AND layer = ?`,
		req.ParentGUID[:], foldedName, req.LayerName); err != nil {
		log.Printf("delete entry (volatile): %v", err)
		return rsi.StatusStorageError, nil
	}

	return rsi.StatusOK, nil
}

func (h *Handler) handleEnumChildren(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeEnumChildrenRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive := h.resolveHiveByParent(req.ParentGUID)
	if hive == nil {
		return rsi.StatusNotFound, nil
	}

	db, err := h.readQ(hdr, hive)
	if err != nil {
		return mapReadErr("enum children", err), nil
	}

	rows, err := db.Query(`
		SELECT child_name, child_name_folded, layer, target_type, target_guid, sequence
		FROM main.path_entries WHERE parent_guid = ?
		UNION ALL
		SELECT child_name, child_name_folded, layer, target_type, target_guid, sequence
		FROM volatile.path_entries WHERE parent_guid = ?
	`, req.ParentGUID[:], req.ParentGUID[:])
	if err != nil {
		log.Printf("enum children query error: %v", err)
		return rsi.StatusStorageError, nil
	}
	defer rows.Close()

	type childGroup struct {
		displayName string
		entries     []rsi.LookupPathEntry
	}
	groups := make(map[string]*childGroup)
	guidSet := make(map[rsi.GUID]bool)

	for rows.Next() {
		var childName, childNameFolded, layerName string
		var targetType uint8
		var targetGUID []byte
		var sequence uint64

		if err := rows.Scan(&childName, &childNameFolded, &layerName, &targetType, &targetGUID, &sequence); err != nil {
			log.Printf("enum children scan error: %v", err)
			return rsi.StatusStorageError, nil
		}

		entry := rsi.LookupPathEntry{
			LayerName:  layerName,
			TargetType: targetType,
			Sequence:   sequence,
		}
		if targetType == rsi.TargetGUID && targetGUID != nil {
			copy(entry.TargetGUID[:], targetGUID)
			guidSet[entry.TargetGUID] = true
		}

		g, ok := groups[childNameFolded]
		if !ok {
			g = &childGroup{displayName: childName}
			groups[childNameFolded] = g
		}
		g.entries = append(g.entries, entry)
	}
	if err := rows.Err(); err != nil {
		log.Printf("enum children iteration error: %v", err)
		return rsi.StatusStorageError, nil
	}

	children := make([]rsi.EnumChildrenChild, 0, len(groups))
	for _, g := range groups {
		children = append(children, rsi.EnumChildrenChild{
			ChildName: g.displayName,
			Entries:   g.entries,
		})
	}

	var meta []rsi.LookupKeyMeta
	for guid := range guidSet {
		m, err := readKeyMeta(db, guid)
		if err != nil {
			log.Printf("enum children meta error for %x: %v", guid, err)
			return rsi.StatusStorageError, nil
		}
		meta = append(meta, m)
	}

	enc := rsi.NewEncoder(512)
	rsi.EncodeEnumChildrenResponse(enc, children, meta)
	return rsi.StatusOK, enc.Bytes()
}

// --- Key operations ---

func (h *Handler) handleCreateKey(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeCreateKeyRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive := h.resolveHive(req.ParentGUID)
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
	if req.Volatile {
		table = "volatile.keys"
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
		return rsi.StatusStorageError, nil
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

func (h *Handler) handleReadKey(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeReadKeyRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive := h.resolveHive(req.GUID)
	if hive == nil {
		return rsi.StatusNotFound, nil
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
		return rsi.StatusStorageError, nil
	}

	enc := rsi.NewEncoder(128)
	rsi.EncodeReadKeyResponse(enc, m)
	return rsi.StatusOK, enc.Bytes()
}

func (h *Handler) handleWriteKey(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeWriteKeyRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	if req.FieldMask & ^uint32(rsi.WriteKeyFieldSD|rsi.WriteKeyFieldLastWriteTime) != 0 {
		return rsi.StatusInvalid, nil
	}

	hive := h.resolveHive(req.GUID)
	if hive == nil {
		return rsi.StatusNotFound, nil
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
		return rsi.StatusStorageError, nil
	}
	if vol {
		table = "volatile.keys"
	}

	var result sql.Result
	if req.FieldMask&rsi.WriteKeyFieldSD != 0 && req.FieldMask&rsi.WriteKeyFieldLastWriteTime != 0 {
		result, err = wq.Exec(`UPDATE `+table+` SET sd = ?, last_write_time = ? WHERE guid = ?`, req.SD, req.LastWriteTime, req.GUID[:])
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
			return rsi.StatusStorageError, nil
		}
		return rsi.StatusOK, nil
	}

	if err != nil {
		log.Printf("write key error: %v", err)
		return rsi.StatusStorageError, nil
	}

	// Check that the UPDATE matched a row.
	if n, _ := result.RowsAffected(); n == 0 {
		return rsi.StatusNotFound, nil
	}

	return rsi.StatusOK, nil
}

func (h *Handler) handleDropKey(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeDropKeyRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive := h.resolveHive(req.GUID)
	if hive == nil {
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
		tx, err := hive.WriteDB().Begin()
		if err != nil {
			log.Printf("drop key begin tx: %v", err)
			return rsi.StatusStorageError, nil
		}
		defer tx.Rollback()
		for _, q := range dropDeletes {
			if _, err := tx.Exec(q, req.GUID[:]); err != nil {
				log.Printf("drop key: %v", err)
				return rsi.StatusStorageError, nil
			}
		}
		if err := tx.Commit(); err != nil {
			log.Printf("drop key commit: %v", err)
			return rsi.StatusStorageError, nil
		}
	} else {
		for _, q := range dropDeletes {
			if _, err := wq.Exec(q, req.GUID[:]); err != nil {
				log.Printf("drop key (txn): %v", err)
				return rsi.StatusStorageError, nil
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
