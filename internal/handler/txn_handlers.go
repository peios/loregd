package handler

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"sort"

	"github.com/peios/loregd/internal/fold"
	"github.com/peios/loregd/internal/hivedb"
	"github.com/peios/loregd/internal/rsi"
)

func (h *Handler) registerTxnHandlers(d *rsi.Dispatcher) {
	d.Register(rsi.OpBeginTransaction, h.handleBeginTransaction)
	d.Register(rsi.OpCommitTransaction, h.handleCommitTransaction)
	d.Register(rsi.OpAbortTransaction, h.handleAbortTransaction)
	d.Register(rsi.OpDeleteLayer, h.handleDeleteLayer)
	d.Register(rsi.OpFlush, h.handleFlush)
}

func (h *Handler) handleBeginTransaction(header rsi.RequestHeader, payload []byte) (status uint32, response []byte) {
	header.TxnID = 0 // transaction control identifies the transaction in its payload
	hdr, err := h.startRequest(header)
	if err != nil {
		return mapWriteErr("request", err), nil
	}
	defer h.finishRequest(hdr, &status)
	req, err := rsi.DecodeBeginTransactionRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}
	if req.Mode != rsi.TxnReadWrite && req.Mode != rsi.TxnReadOnly {
		return rsi.StatusInvalid, nil
	}

	if err := h.txns.beginLeased(req.TxnID, req.Mode == rsi.TxnReadOnly, header); err != nil {
		log.Printf("begin transaction %d: %v", req.TxnID, err)
		return rsi.StatusInvalid, nil
	}
	return rsi.StatusOK, nil
}

func (h *Handler) handleCommitTransaction(header rsi.RequestHeader, payload []byte) (status uint32, response []byte) {
	if header.Session != 0 && header.LeaseNS == 0 {
		return rsi.StatusInvalid, nil
	}
	header.TxnID = 0 // transaction control identifies the transaction in its payload
	hdr, err := h.startRequest(header)
	if err != nil {
		return mapWriteErr("request", err), nil
	}
	defer hdr.close() // commit owns its outcome after admission
	req, err := rsi.DecodeCommitTransactionRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	if err := h.txns.commit(hdr.ctx, req.TxnID); err != nil {
		if isBusy(err) {
			return rsi.StatusTxnBusy, nil
		}
		log.Printf("commit transaction %d: %v", req.TxnID, err)
		return storageStatus(err), nil
	}

	return rsi.StatusOK, nil
}

func (h *Handler) handleAbortTransaction(header rsi.RequestHeader, payload []byte) (status uint32, response []byte) {
	header.TxnID = 0 // transaction control identifies the transaction in its payload
	hdr, err := h.startRequest(header)
	if err != nil {
		return mapWriteErr("request", err), nil
	}
	defer h.finishRequest(hdr, &status)
	req, err := rsi.DecodeAbortTransactionRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	// abort() always succeeds per spec.
	h.txns.abort(req.TxnID)
	return rsi.StatusOK, nil
}

func (h *Handler) handleDeleteLayer(header rsi.RequestHeader, payload []byte) (status uint32, response []byte) {
	hdr, err := h.startRequest(header)
	if err != nil {
		return mapWriteErr("request", err), nil
	}
	defer h.finishRequest(hdr, &status)
	req, err := rsi.DecodeDeleteLayerRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	// This deletes data, so it is refused under a read-only transaction like
	// every other mutating operation. It cannot *join* a read-write one — it
	// spans every hive and a transaction binds to exactly one — so it is
	// checked here rather than routed through writeQ.
	if err := h.txns.mutationAllowed(hdr.TxnID); err != nil {
		return mapWriteErr("delete layer", err), nil
	}

	// Each hive allows a single write connection, so opening one here while a
	// transaction holds it blocks indefinitely. Decline instead: the caller can
	// commit and retry, which is the same answer RSI_FLUSH already gives.
	if h.txns.anyWriteBound() {
		return rsi.StatusTxnBusy, nil
	}

	var allOrphans []rsi.GUID

	// RSI_DELETE_LAYER applies to ALL hives.
	for _, hive := range h.hives {
		unlock, err := hive.Gate.Acquire(hdr.ctx, true)
		if err != nil {
			return mapWriteErr("delete layer", err), nil
		}
		if h.txns.otherWriter(hive, 0) {
			unlock()
			return rsi.StatusTxnBusy, nil
		}
		orphans, err := deleteLayerFromHive(hdr.ctx, hive, req.LayerName)
		unlock()
		if err != nil {
			// A contended write is RSI_TXN_BUSY, not a storage error — the
			// same distinction every other write path draws.
			if isBusy(err) {
				return rsi.StatusTxnBusy, nil
			}
			log.Printf("delete layer %q from %s: %v", req.LayerName, hive.Name, err)
			return storageStatus(err), nil
		}
		allOrphans = append(allOrphans, orphans...)
	}

	// The hive walk above ranges a Go map, so identical calls produced
	// the concatenated orphans in different orders. Sort bytewise, as
	// sortedGUIDs already does for every other GUID array on the wire.
	sort.Slice(allOrphans, func(i, j int) bool {
		return bytes.Compare(allOrphans[i][:], allOrphans[j][:]) < 0
	})

	// Evict orphaned GUIDs from cache. The key records still exist
	// but are unreachable; LCS will issue RSI_DROP_KEY for each.
	for _, guid := range allOrphans {
		h.guidCache.Delete(guid)
	}

	enc := rsi.NewEncoder(64)
	rsi.EncodeDeleteLayerResponse(enc, allOrphans)
	return rsi.StatusOK, enc.Bytes()
}

func (h *Handler) handleFlush(header rsi.RequestHeader, payload []byte) (status uint32, response []byte) {
	hdr, err := h.startRequest(header)
	if err != nil {
		return mapWriteErr("request", err), nil
	}
	defer h.finishRequest(hdr, &status)
	req, err := rsi.DecodeFlushRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	if err := h.txns.mutationAllowed(hdr.TxnID); err != nil {
		return mapWriteErr("flush", err), nil
	}

	foldedName := fold.String(req.HiveName)
	hive, ok := h.hives[foldedName]
	if !ok {
		return rsi.StatusInvalid, nil
	}

	if err := hdr.lockHive(hive, true); err != nil {
		return mapWriteErr("flush", err), nil
	}

	// If a transaction holds the write connection for this hive,
	// attempting a checkpoint would deadlock (MaxOpenConns=1).
	if h.txns.hiveBusy(hive) {
		return rsi.StatusTxnBusy, nil
	}

	var busy, logPages, checkpointed int
	err = hive.WriteDB().QueryRowContext(hdr.ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &checkpointed)
	if err != nil {
		log.Printf("flush %s: %v", hive.Name, err)
		return storageStatus(err), nil
	}
	if busy != 0 {
		log.Printf("flush %s: checkpoint blocked by readers (log=%d, checkpointed=%d)", hive.Name, logPages, checkpointed)
		return rsi.StatusTxnBusy, nil
	}

	return rsi.StatusOK, nil
}

// deleteLayerFromHive removes all entries for a layer from one hive,
// within a transaction. Returns the set of orphaned GUIDs.
func deleteLayerFromHive(ctx context.Context, hive *hivedb.HiveDB, layerName string) ([]rsi.GUID, error) {

	// Use a pinned connection with BEGIN IMMEDIATE to prevent
	// concurrent writers from interleaving between the orphan-
	// detection read and the DELETEs.
	conn, err := hive.WriteDB().Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire write connection: %w", err)
	}
	defer conn.Close()
	committed := false
	defer func() {
		if !committed {
			rollbackConn(conn)
		}
	}()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("begin immediate: %w", err)
	}

	// Capture orphaned GUIDs before deletion.
	rows, err := conn.QueryContext(ctx, `
		SELECT DISTINCT target_guid FROM (
			SELECT target_guid FROM main.path_entries
			WHERE layer = ? AND target_type = 0
			UNION ALL
			SELECT target_guid FROM volatile.path_entries
			WHERE layer = ? AND target_type = 0
		)
		WHERE target_guid NOT IN (
			SELECT target_guid FROM main.path_entries
			WHERE layer != ? AND target_type = 0
			UNION
			SELECT target_guid FROM volatile.path_entries
			WHERE layer != ? AND target_type = 0
		)
	`, layerName, layerName, layerName, layerName)
	if err != nil {
		return nil, err
	}

	var orphans []rsi.GUID
	for rows.Next() {
		var guidBytes []byte
		if err := rows.Scan(&guidBytes); err != nil {
			rows.Close()
			return nil, err
		}
		var g rsi.GUID
		if len(guidBytes) == 16 {
			copy(g[:], guidBytes)
		}
		orphans = append(orphans, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orphan scan: %w", err)
	}

	// Delete all entries for the layer, checking each error.
	deletes := []struct {
		query string
		desc  string
	}{
		{`DELETE FROM main.path_entries WHERE layer = ?`, "main.path_entries"},
		{`DELETE FROM main.[values] WHERE layer = ?`, "main.values"},
		{`DELETE FROM main.blanket_tombstones WHERE layer = ?`, "main.blanket_tombstones"},
		{`DELETE FROM volatile.path_entries WHERE layer = ?`, "volatile.path_entries"},
		{`DELETE FROM volatile.[values] WHERE layer = ?`, "volatile.values"},
		{`DELETE FROM volatile.blanket_tombstones WHERE layer = ?`, "volatile.blanket_tombstones"},
	}
	for _, d := range deletes {
		if _, err := conn.ExecContext(ctx, d.query, layerName); err != nil {
			return nil, fmt.Errorf("delete %s: %w", d.desc, err)
		}
	}

	if err := commitConn(ctx, conn); err != nil {
		return nil, err
	}

	committed = true
	return orphans, nil
}
