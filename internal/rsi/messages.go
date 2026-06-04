package rsi

// Request types for all RSI operations.
// Each Decode* function parses the payload (bytes after the 22-byte header).
// Each Encode* function produces the response payload (after the status field).

// --- Path operations ---

type LookupRequest struct {
	ParentGUID GUID
	ChildName  string
}

func DecodeLookupRequest(d *Decoder) (LookupRequest, error) {
	var r LookupRequest
	var err error
	if r.ParentGUID, err = d.GUID(); err != nil {
		return r, err
	}
	if r.ChildName, err = d.String(); err != nil {
		return r, err
	}
	return r, nil
}

// LookupPathEntry is one entry in a Lookup/EnumChildren response.
type LookupPathEntry struct {
	LayerName  string
	TargetType uint8
	TargetGUID GUID
	Sequence   uint64
}

// LookupKeyMeta is key metadata included in Lookup/EnumChildren responses.
type LookupKeyMeta struct {
	GUID          GUID
	SD            []byte
	Volatile      bool
	Symlink       bool
	LastWriteTime int64
}

func EncodeLookupResponse(e *Encoder, entries []LookupPathEntry, meta []LookupKeyMeta) {
	e.PutUint32(uint32(len(entries)))
	for _, ent := range entries {
		e.PutString(ent.LayerName)
		e.PutUint8(ent.TargetType)
		e.PutGUID(ent.TargetGUID)
		e.PutUint64(ent.Sequence)
	}
	e.PutUint32(uint32(len(meta)))
	for _, m := range meta {
		e.PutGUID(m.GUID)
		e.PutBlob(m.SD)
		e.PutBool(m.Volatile)
		e.PutBool(m.Symlink)
		e.PutUint64(uint64(m.LastWriteTime))
	}
}

type CreateEntryRequest struct {
	ParentGUID GUID
	ChildName  string
	LayerName  string
	ChildGUID  GUID
	Sequence   uint64
}

func DecodeCreateEntryRequest(d *Decoder) (CreateEntryRequest, error) {
	var r CreateEntryRequest
	var err error
	if r.ParentGUID, err = d.GUID(); err != nil {
		return r, err
	}
	if r.ChildName, err = d.String(); err != nil {
		return r, err
	}
	if r.LayerName, err = d.String(); err != nil {
		return r, err
	}
	if r.ChildGUID, err = d.GUID(); err != nil {
		return r, err
	}
	if r.Sequence, err = d.Uint64(); err != nil {
		return r, err
	}
	return r, nil
}

type HideEntryRequest struct {
	ParentGUID GUID
	ChildName  string
	LayerName  string
	Sequence   uint64
}

func DecodeHideEntryRequest(d *Decoder) (HideEntryRequest, error) {
	var r HideEntryRequest
	var err error
	if r.ParentGUID, err = d.GUID(); err != nil {
		return r, err
	}
	if r.ChildName, err = d.String(); err != nil {
		return r, err
	}
	if r.LayerName, err = d.String(); err != nil {
		return r, err
	}
	if r.Sequence, err = d.Uint64(); err != nil {
		return r, err
	}
	return r, nil
}

type DeleteEntryRequest struct {
	ParentGUID GUID
	ChildName  string
	LayerName  string
}

func DecodeDeleteEntryRequest(d *Decoder) (DeleteEntryRequest, error) {
	var r DeleteEntryRequest
	var err error
	if r.ParentGUID, err = d.GUID(); err != nil {
		return r, err
	}
	if r.ChildName, err = d.String(); err != nil {
		return r, err
	}
	if r.LayerName, err = d.String(); err != nil {
		return r, err
	}
	return r, nil
}

type EnumChildrenRequest struct {
	ParentGUID GUID
}

func DecodeEnumChildrenRequest(d *Decoder) (EnumChildrenRequest, error) {
	var r EnumChildrenRequest
	var err error
	if r.ParentGUID, err = d.GUID(); err != nil {
		return r, err
	}
	return r, nil
}

// EnumChildrenChild groups entries for one child name.
type EnumChildrenChild struct {
	ChildName string
	Entries   []LookupPathEntry
}

func EncodeEnumChildrenResponse(e *Encoder, children []EnumChildrenChild, meta []LookupKeyMeta) {
	e.PutUint32(uint32(len(children)))
	for _, ch := range children {
		e.PutString(ch.ChildName)
		e.PutUint32(uint32(len(ch.Entries)))
		for _, ent := range ch.Entries {
			e.PutString(ent.LayerName)
			e.PutUint8(ent.TargetType)
			e.PutGUID(ent.TargetGUID)
			e.PutUint64(ent.Sequence)
		}
	}
	e.PutUint32(uint32(len(meta)))
	for _, m := range meta {
		e.PutGUID(m.GUID)
		e.PutBlob(m.SD)
		e.PutBool(m.Volatile)
		e.PutBool(m.Symlink)
		e.PutUint64(uint64(m.LastWriteTime))
	}
}

// --- Key operations ---

type CreateKeyRequest struct {
	GUID       GUID
	Name       string
	ParentGUID GUID
	SD         []byte
	Volatile   bool
	Symlink    bool
}

func DecodeCreateKeyRequest(d *Decoder) (CreateKeyRequest, error) {
	var r CreateKeyRequest
	var err error
	if r.GUID, err = d.GUID(); err != nil {
		return r, err
	}
	if r.Name, err = d.String(); err != nil {
		return r, err
	}
	if r.ParentGUID, err = d.GUID(); err != nil {
		return r, err
	}
	if r.SD, err = d.Blob(); err != nil {
		return r, err
	}
	v, err := d.Bool()
	if err != nil {
		return r, err
	}
	r.Volatile = v
	s, err := d.Bool()
	if err != nil {
		return r, err
	}
	r.Symlink = s
	return r, nil
}

type ReadKeyRequest struct {
	GUID GUID
}

func DecodeReadKeyRequest(d *Decoder) (ReadKeyRequest, error) {
	var r ReadKeyRequest
	var err error
	if r.GUID, err = d.GUID(); err != nil {
		return r, err
	}
	return r, nil
}

type ReadKeyResponse struct {
	Name          string
	ParentGUID    GUID
	SD            []byte
	Volatile      bool
	Symlink       bool
	LastWriteTime int64
}

func EncodeReadKeyResponse(e *Encoder, r ReadKeyResponse) {
	e.PutString(r.Name)
	e.PutGUID(r.ParentGUID)
	e.PutBlob(r.SD)
	e.PutBool(r.Volatile)
	e.PutBool(r.Symlink)
	e.PutUint64(uint64(r.LastWriteTime))
}

type WriteKeyRequest struct {
	GUID          GUID
	FieldMask     uint32
	SD            []byte // present if bit 0 set
	LastWriteTime int64  // present if bit 1 set
}

func DecodeWriteKeyRequest(d *Decoder) (WriteKeyRequest, error) {
	var r WriteKeyRequest
	var err error
	if r.GUID, err = d.GUID(); err != nil {
		return r, err
	}
	if r.FieldMask, err = d.Uint32(); err != nil {
		return r, err
	}
	if r.FieldMask&WriteKeyFieldSD != 0 {
		if r.SD, err = d.Blob(); err != nil {
			return r, err
		}
	}
	if r.FieldMask&WriteKeyFieldLastWriteTime != 0 {
		v, err := d.Uint64()
		if err != nil {
			return r, err
		}
		r.LastWriteTime = int64(v)
	}
	return r, nil
}

type DropKeyRequest struct {
	GUID GUID
}

func DecodeDropKeyRequest(d *Decoder) (DropKeyRequest, error) {
	var r DropKeyRequest
	var err error
	if r.GUID, err = d.GUID(); err != nil {
		return r, err
	}
	return r, nil
}

// --- Value operations ---

type QueryValuesRequest struct {
	GUID      GUID
	ValueName string
	QueryAll  bool
}

func DecodeQueryValuesRequest(d *Decoder) (QueryValuesRequest, error) {
	var r QueryValuesRequest
	var err error
	if r.GUID, err = d.GUID(); err != nil {
		return r, err
	}
	if r.ValueName, err = d.String(); err != nil {
		return r, err
	}
	qa, err := d.Bool()
	if err != nil {
		return r, err
	}
	r.QueryAll = qa
	return r, nil
}

type QueryValuesEntry struct {
	ValueName string
	LayerName string
	Type      uint32
	Data      []byte
	Sequence  uint64
}

type BlanketTombstoneEntry struct {
	LayerName string
	Sequence  uint64
}

func EncodeQueryValuesResponse(e *Encoder, entries []QueryValuesEntry, blankets []BlanketTombstoneEntry) {
	e.PutUint32(uint32(len(entries)))
	for _, ent := range entries {
		e.PutString(ent.ValueName)
		e.PutString(ent.LayerName)
		e.PutUint32(ent.Type)
		e.PutBlob(ent.Data)
		e.PutUint64(ent.Sequence)
	}
	e.PutUint32(uint32(len(blankets)))
	for _, bt := range blankets {
		e.PutString(bt.LayerName)
		e.PutUint64(bt.Sequence)
	}
}

type SetValueRequest struct {
	GUID             GUID
	ValueName        string
	LayerName        string
	Type             uint32
	Data             []byte
	Sequence         uint64
	ExpectedSequence uint64
}

func DecodeSetValueRequest(d *Decoder) (SetValueRequest, error) {
	var r SetValueRequest
	var err error
	if r.GUID, err = d.GUID(); err != nil {
		return r, err
	}
	if r.ValueName, err = d.String(); err != nil {
		return r, err
	}
	if r.LayerName, err = d.String(); err != nil {
		return r, err
	}
	if r.Type, err = d.Uint32(); err != nil {
		return r, err
	}
	if r.Data, err = d.Blob(); err != nil {
		return r, err
	}
	if r.Sequence, err = d.Uint64(); err != nil {
		return r, err
	}
	if r.ExpectedSequence, err = d.Uint64(); err != nil {
		return r, err
	}
	return r, nil
}

type DeleteValueEntryRequest struct {
	GUID      GUID
	ValueName string
	LayerName string
}

func DecodeDeleteValueEntryRequest(d *Decoder) (DeleteValueEntryRequest, error) {
	var r DeleteValueEntryRequest
	var err error
	if r.GUID, err = d.GUID(); err != nil {
		return r, err
	}
	if r.ValueName, err = d.String(); err != nil {
		return r, err
	}
	if r.LayerName, err = d.String(); err != nil {
		return r, err
	}
	return r, nil
}

type SetBlanketTombstoneRequest struct {
	GUID      GUID
	LayerName string
	Set       bool
	Sequence  uint64
}

func DecodeSetBlanketTombstoneRequest(d *Decoder) (SetBlanketTombstoneRequest, error) {
	var r SetBlanketTombstoneRequest
	var err error
	if r.GUID, err = d.GUID(); err != nil {
		return r, err
	}
	if r.LayerName, err = d.String(); err != nil {
		return r, err
	}
	s, err := d.Bool()
	if err != nil {
		return r, err
	}
	r.Set = s
	if r.Sequence, err = d.Uint64(); err != nil {
		return r, err
	}
	return r, nil
}

// --- Transaction operations ---

type BeginTransactionRequest struct {
	TxnID uint64
	Mode  uint32 // TxnReadWrite (0) or TxnReadOnly (1)
}

func DecodeBeginTransactionRequest(d *Decoder) (BeginTransactionRequest, error) {
	var r BeginTransactionRequest
	var err error
	if r.TxnID, err = d.Uint64(); err != nil {
		return r, err
	}
	// LCS always sends the mode field (PSD-005 §11.3). Tolerate its
	// absence (defaulting to read-write) so the source-agnostic begin
	// path and older callers remain valid.
	if d.Remaining() >= 4 {
		if r.Mode, err = d.Uint32(); err != nil {
			return r, err
		}
	}
	return r, nil
}

type CommitTransactionRequest struct {
	TxnID uint64
}

func DecodeCommitTransactionRequest(d *Decoder) (CommitTransactionRequest, error) {
	var r CommitTransactionRequest
	var err error
	if r.TxnID, err = d.Uint64(); err != nil {
		return r, err
	}
	return r, nil
}

type AbortTransactionRequest struct {
	TxnID uint64
}

func DecodeAbortTransactionRequest(d *Decoder) (AbortTransactionRequest, error) {
	var r AbortTransactionRequest
	var err error
	if r.TxnID, err = d.Uint64(); err != nil {
		return r, err
	}
	return r, nil
}

// --- Layer operations ---

type DeleteLayerRequest struct {
	LayerName string
}

func DecodeDeleteLayerRequest(d *Decoder) (DeleteLayerRequest, error) {
	var r DeleteLayerRequest
	var err error
	if r.LayerName, err = d.String(); err != nil {
		return r, err
	}
	return r, nil
}

func EncodeDeleteLayerResponse(e *Encoder, orphanedGUIDs []GUID) {
	e.PutUint32(uint32(len(orphanedGUIDs)))
	for _, g := range orphanedGUIDs {
		e.PutGUID(g)
	}
}

// --- Maintenance operations ---

type FlushRequest struct {
	HiveName string
}

func DecodeFlushRequest(d *Decoder) (FlushRequest, error) {
	var r FlushRequest
	var err error
	if r.HiveName, err = d.String(); err != nil {
		return r, err
	}
	return r, nil
}
