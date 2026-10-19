package handler

import (
	"bytes"
	"database/sql"
	"fmt"
	"log"
	"sort"

	"github.com/peios/loregd/internal/fold"
	"github.com/peios/loregd/internal/hivedb"
	"github.com/peios/loregd/internal/rsi"
)

// RSI enumeration responses MUST be deterministically ordered. Consumers (the
// LCS) walk RSI_LOOKUP / RSI_ENUM_CHILDREN / RSI_QUERY_VALUES results by dense
// index across repeated, independent RSI calls, so an unstable order makes that
// walk duplicate or skip entries — the source returning the same query in a
// different order each time is a contract violation (PSD-006 §5). The SQL
// UNION ALL queries and the Go grouping map are both unordered, so loregd
// imposes a canonical order on every result set before it is encoded. These
// helpers are that canonicalisation.

// sortedGUIDs returns the GUIDs of set in ascending byte order.
func sortedGUIDs(set map[rsi.GUID]bool) []rsi.GUID {
	guids := make([]rsi.GUID, 0, len(set))
	for g := range set {
		guids = append(guids, g)
	}
	sort.Slice(guids, func(i, j int) bool {
		return bytes.Compare(guids[i][:], guids[j][:]) < 0
	})
	return guids
}

// sortPathEntries orders per-layer path entries by (layer, sequence). Layer
// resolution itself is order-independent (it selects a max), but a stable wire
// order keeps the whole response reproducible.
func sortPathEntries(entries []rsi.LookupPathEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].LayerName != entries[j].LayerName {
			return entries[i].LayerName < entries[j].LayerName
		}
		return entries[i].Sequence < entries[j].Sequence
	})
}

// --- Path operations ---

func (h *Handler) handleLookup(header rsi.RequestHeader, payload []byte) (status uint32, response []byte) {
	hdr, err := h.startRequest(header)
	if err != nil {
		return mapWriteErr("request", err), nil
	}
	defer h.finishRequest(hdr, &status)
	req, err := rsi.DecodeLookupRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive, st, ok := h.resolveHiveStatus(hdr, "resolveHiveByParent", req.ParentGUID)
	if !ok {
		return st, nil
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
		return storageStatus(err), nil
	}
	defer closeReadRows(rows)

	var entries []rsi.LookupPathEntry
	guidSet := make(map[rsi.GUID]bool)

	for rows.Next() {
		var e rsi.LookupPathEntry
		var targetGUID []byte
		if err := rows.Scan(&e.LayerName, &e.TargetType, &targetGUID, &e.Sequence); err != nil {
			log.Printf("lookup scan error: %v", err)
			return storageStatus(err), nil
		}
		if e.TargetType == rsi.TargetGUID && targetGUID != nil {
			copy(e.TargetGUID[:], targetGUID)
			guidSet[e.TargetGUID] = true
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		log.Printf("lookup iteration error: %v", err)
		return storageStatus(err), nil
	}
	sortPathEntries(entries)

	// A key is created as two requests — REG create-entry (the path entry)
	// then create-key (the key row) — outside any transaction, so a lookup
	// landing between them finds an entry whose target has no key row yet.
	// That is "not there yet", not a storage failure: drop the dangling
	// entry from the answer and the caller sees the child as absent, exactly
	// as it would have a moment earlier. Reporting StatusStorageError here
	// aborted the kernel's registration bootstrap when Phase-1.5 autoapply
	// raced it, leaving no self-watch armed for the whole boot (PEI-510).
	meta, dangling, err := readReferencedMetadata(db, guidSet, "lookup")
	if err != nil {
		log.Print(err)
		return storageStatus(err), nil
	}

	if len(dangling) > 0 {
		entries = removeDanglingEntries(entries, dangling)
	}

	enc := rsi.NewEncoder(256)
	rsi.EncodeLookupResponse(enc, entries, meta)
	return rsi.StatusOK, enc.Bytes()
}

func (h *Handler) handleCreateEntry(header rsi.RequestHeader, payload []byte) (status uint32, response []byte) {
	hdr, err := h.startRequest(header)
	if err != nil {
		return mapWriteErr("request", err), nil
	}
	defer h.finishRequest(hdr, &status)
	req, err := rsi.DecodeCreateEntryRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive, st, ok := h.resolveHiveStatus(hdr, "resolveHiveByParent", req.ParentGUID)
	if !ok {
		return st, nil
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
		// requires LCS to create the key before the entry, or a volatile flag
		// to the CREATE_ENTRY wire request.
		vol = false
	} else if volErr != nil {
		log.Printf("create entry isVolatile: %v", volErr)
		return storageStatus(volErr), nil
	}
	other := "volatile"
	if vol {
		table = "volatile.path_entries"
		other = "main"
	}

	// As for keys: the PK on (parent_guid, child_name_folded, layer) binds one
	// schema, so the same triple could exist in both. Nothing de-duplicates on
	// read, so RSI_LOOKUP and RSI_ENUM_CHILDREN emitted both rows inside one
	// child block -- two entries claiming the same name in the same layer,
	// which the layer model does not expect.
	if exists, err := pathEntryExistsInQ(wq, other, req.ParentGUID, foldedName, req.LayerName); err != nil {
		log.Printf("create entry cross-schema check: %v", err)
		return storageStatus(err), nil
	} else if exists {
		return rsi.StatusAlreadyExists, nil
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
		return storageStatus(err), nil
	}

	return rsi.StatusOK, nil
}

func (h *Handler) handleHideEntry(header rsi.RequestHeader, payload []byte) (status uint32, response []byte) {
	hdr, err := h.startRequest(header)
	if err != nil {
		return mapWriteErr("request", err), nil
	}
	defer h.finishRequest(hdr, &status)
	req, err := rsi.DecodeHideEntryRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive, st, ok := h.resolveHiveStatus(hdr, "resolveHiveByParent", req.ParentGUID)
	if !ok {
		return st, nil
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
		return storageStatus(volErr), nil
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
		return storageStatus(err), nil
	}

	return rsi.StatusOK, nil
}

func (h *Handler) handleDeleteEntry(header rsi.RequestHeader, payload []byte) (status uint32, response []byte) {
	hdr, err := h.startRequest(header)
	if err != nil {
		return mapWriteErr("request", err), nil
	}
	defer h.finishRequest(hdr, &status)
	req, err := rsi.DecodeDeleteEntryRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive, st, ok := h.resolveHiveStatus(hdr, "resolveHiveByParent", req.ParentGUID)
	if !ok {
		return st, nil
	}

	wq, err := h.writeQ(hdr, hive)
	if err != nil {
		return mapWriteErr("delete entry", err), nil
	}

	foldedName := fold.String(req.ChildName)

	if _, err := wq.Exec(`DELETE FROM main.path_entries WHERE parent_guid = ? AND child_name_folded = ? AND layer = ?`,
		req.ParentGUID[:], foldedName, req.LayerName); err != nil {
		log.Printf("delete entry (main): %v", err)
		return storageStatus(err), nil
	}
	if _, err := wq.Exec(`DELETE FROM volatile.path_entries WHERE parent_guid = ? AND child_name_folded = ? AND layer = ?`,
		req.ParentGUID[:], foldedName, req.LayerName); err != nil {
		log.Printf("delete entry (volatile): %v", err)
		return storageStatus(err), nil
	}

	return rsi.StatusOK, nil
}

func (h *Handler) handleEnumChildren(header rsi.RequestHeader, payload []byte) (status uint32, response []byte) {
	hdr, err := h.startRequest(header)
	if err != nil {
		return mapWriteErr("request", err), nil
	}
	defer h.finishRequest(hdr, &status)
	req, err := rsi.DecodeEnumChildrenRequest(rsi.NewDecoder(payload))
	if err != nil {
		return rsi.StatusInvalid, nil
	}

	hive, st, ok := h.resolveHiveStatus(hdr, "resolveHiveByParent", req.ParentGUID)
	if !ok {
		return st, nil
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
		return storageStatus(err), nil
	}
	defer closeReadRows(rows)

	groups, guidSet, err := readChildGroups(rows)
	if err != nil {
		log.Print(err)
		return storageStatus(err), nil
	}
	children := orderedChildren(groups)

	// The window RSI_LOOKUP already tolerates reaches enumeration too: LCS
	// creates a key as create-entry then create-key outside any transaction,
	// so a parent enumerated between the two holds an entry whose key row
	// does not exist yet. Failing the request reported a storage fault where
	// the storage is fine, and left the caller unable to tell a transient
	// race from real corruption.
	//
	// The entry cannot simply be emitted without its metadata: the kernel
	// rejects a GUID-typed entry whose metadata block is missing *and* a
	// metadata block nothing references, tearing the source down either way
	// (validate_rsi_enum_children_metadata_completeness). So the entry is
	// dropped, exactly as on lookup, and a child left holding no entries at
	// all drops with it — the child reads as absent, which is what it was a
	// moment earlier (PEI-233).
	meta, dangling, err := readReferencedMetadata(db, guidSet, "enum children")
	if err != nil {
		log.Print(err)
		return storageStatus(err), nil
	}

	if len(dangling) > 0 {
		children = removeDanglingChildren(children, dangling)
	}

	enc := rsi.NewEncoder(512)
	rsi.EncodeEnumChildrenResponse(enc, children, meta)
	return rsi.StatusOK, enc.Bytes()
}

// readReferencedMetadata omits dangling path targets during the create-entry /
// create-key window. Every emitted GUID entry must have exactly one metadata row.
func readReferencedMetadata(db Querier, guids map[rsi.GUID]bool, operation string) ([]rsi.LookupKeyMeta, map[rsi.GUID]bool, error) {
	var metadata []rsi.LookupKeyMeta
	dangling := make(map[rsi.GUID]bool)
	for _, guid := range sortedGUIDs(guids) {
		entry, err := readKeyMeta(db, guid)
		if errors.Is(err, sql.ErrNoRows) {
			dangling[guid] = true
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%s meta error for %x: %w", operation, guid, err)
		}
		metadata = append(metadata, entry)
	}
	return metadata, dangling, nil
}

func removeDanglingEntries(entries []rsi.LookupPathEntry, dangling map[rsi.GUID]bool) []rsi.LookupPathEntry {
	kept := entries[:0]
	for _, entry := range entries {
		if entry.TargetType == rsi.TargetGUID && dangling[entry.TargetGUID] {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

func removeDanglingChildren(children []rsi.EnumChildrenChild, dangling map[rsi.GUID]bool) []rsi.EnumChildrenChild {
	kept := children[:0]
	for _, child := range children {
		child.Entries = removeDanglingEntries(child.Entries, dangling)
		if len(child.Entries) != 0 {
			kept = append(kept, child)
		}
	}
	return kept
}

type childGroup struct {
	displayName string
	entries     []rsi.LookupPathEntry
}

func readChildGroups(rows *sql.Rows) (map[string]*childGroup, map[rsi.GUID]bool, error) {

	groups := make(map[string]*childGroup)
	guidSet := make(map[rsi.GUID]bool)

	for rows.Next() {
		var childName, childNameFolded, layerName string
		var targetType uint8
		var targetGUID []byte
		var sequence uint64

		if err := rows.Scan(&childName, &childNameFolded, &layerName, &targetType, &targetGUID, &sequence); err != nil {
			return nil, nil, fmt.Errorf("enum children scan error: %w", err)
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
		} else if childName < g.displayName {
			// Two rows can share a folded name and differ in stored case
			// — "Foo" in one store, "FOO" in the other — and the union
			// above is unordered, so whichever arrived first decided the
			// case reported. The order of children was stable (it sorts
			// on the folded name) but the name itself was not, so a
			// caller comparing successive enumerations saw it change.
			// Lowest bytewise wins, which is a property of the set rather
			// than of the row order.
			g.displayName = childName
		}
		g.entries = append(g.entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("enum children iteration error: %w", err)
	}

	return groups, guidSet, nil
}

func orderedChildren(groups map[string]*childGroup) []rsi.EnumChildrenChild {

	// Emit children in a deterministic order (ascending folded name). Ranging
	// the groups map directly would yield Go's randomised iteration order, which
	// is the root cause of the dense-index enumeration walk seeing duplicate or
	// missing keys across calls. See the canonicalisation note above.
	foldedNames := make([]string, 0, len(groups))
	for folded := range groups {
		foldedNames = append(foldedNames, folded)
	}
	sort.Strings(foldedNames)

	children := make([]rsi.EnumChildrenChild, 0, len(groups))
	for _, folded := range foldedNames {
		g := groups[folded]
		sortPathEntries(g.entries)
		children = append(children, rsi.EnumChildrenChild{
			ChildName: g.displayName,
			Entries:   g.entries,
		})
	}

	return children
}
