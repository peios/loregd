package handler

import (
	"context"
	"fmt"
	"log"

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

func (h *Handler) handleBeginTransaction(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeBeginTransactionRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	if err := h.txns.beginMode(req.TxnID, req.Mode == rsi.TxnReadOnly); err != nil {
		log.Printf("begin transaction %d: %v", req.TxnID, err)
		return rsi.StatusInvalid, nil
	}
	return rsi.StatusOK, nil
}

func (h *Handler) handleCommitTransaction(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeCommitTransactionRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	if err := h.txns.commit(req.TxnID); err != nil {
		if isBusy(err) {
			return rsi.StatusTxnBusy, nil
		}
		log.Printf("commit transaction %d: %v", req.TxnID, err)
		return rsi.StatusStorageError, nil
	}

	return rsi.StatusOK, nil
}

func (h *Handler) handleAbortTransaction(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeAbortTransactionRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	// abort() always succeeds per spec.
	h.txns.abort(req.TxnID)
	return rsi.StatusOK, nil
}

func (h *Handler) handleDeleteLayer(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeDeleteLayerRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	var allOrphans []rsi.GUID

	// RSI_DELETE_LAYER applies to ALL hives.
	for _, hive := range h.hives {
		orphans, err := deleteLayerFromHive(hive, req.LayerName)
		if err != nil {
			log.Printf("delete layer %q from %s: %v", req.LayerName, hive.Name, err)
			return rsi.StatusStorageError, nil
		}
		allOrphans = append(allOrphans, orphans...)
	}

	// Evict orphaned GUIDs from cache. The key records still exist
	// but are unreachable; LCS will issue RSI_DROP_KEY for each.
	for _, guid := range allOrphans {
		h.guidCache.Delete(guid)
	}

	enc := rsi.NewEncoder(64)
	rsi.EncodeDeleteLayerResponse(enc, allOrphans)
	return rsi.StatusOK, enc.Bytes()
}

func (h *Handler) handleFlush(hdr rsi.RequestHeader, payload []byte) (uint32, []byte) {
	req, err := rsi.DecodeFlushRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	foldedName := fold.String(req.HiveName)
	hive, ok := h.hives[foldedName]
	if !ok {
		return rsi.StatusInvalid, nil
	}

	// If a transaction holds the write connection for this hive,
	// attempting a checkpoint would deadlock (MaxOpenConns=1).
	if h.txns.hiveBusy(hive) {
		return rsi.StatusTxnBusy, nil
	}

	var busy, logPages, checkpointed int
	err = hive.WriteDB().QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &checkpointed)
	if err != nil {
		log.Printf("flush %s: %v", hive.Name, err)
		return rsi.StatusStorageError, nil
	}
	if busy != 0 {
		log.Printf("flush %s: checkpoint blocked by readers (log=%d, checkpointed=%d)", hive.Name, logPages, checkpointed)
		return rsi.StatusTxnBusy, nil
	}

	return rsi.StatusOK, nil
}

// deleteLayerFromHive removes all entries for a layer from one hive,
// within a transaction. Returns the set of orphaned GUIDs.
func deleteLayerFromHive(hive *hivedb.HiveDB, layerName string) ([]rsi.GUID, error) {
	ctx := context.Background()

	// Use a pinned connection with BEGIN IMMEDIATE to prevent
	// concurrent writers from interleaving between the orphan-
	// detection read and the DELETEs.
	conn, err := hive.WriteDB().Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire write connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("begin immediate: %w", err)
	}

	rollback := func() { conn.ExecContext(ctx, "ROLLBACK") }

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
		rollback()
		return nil, err
	}

	var orphans []rsi.GUID
	for rows.Next() {
		var guidBytes []byte
		if err := rows.Scan(&guidBytes); err != nil {
			rows.Close()
			rollback()
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
		rollback()
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
			rollback()
			return nil, fmt.Errorf("delete %s: %w", d.desc, err)
		}
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, err
	}

	return orphans, nil
}
