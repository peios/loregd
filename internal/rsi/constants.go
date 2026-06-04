// Package rsi defines the RSI wire protocol types and constants.
package rsi

// Op codes for RSI operations.
const (
	// Path operations
	OpLookup      uint16 = 0x01
	OpCreateEntry uint16 = 0x02
	OpHideEntry   uint16 = 0x03
	OpDeleteEntry uint16 = 0x04
	OpEnumChildren uint16 = 0x05

	// Key operations
	OpCreateKey uint16 = 0x10
	OpReadKey   uint16 = 0x11
	OpWriteKey  uint16 = 0x12
	OpDropKey   uint16 = 0x13

	// Value operations
	OpQueryValues       uint16 = 0x20
	OpSetValue          uint16 = 0x21
	OpDeleteValueEntry  uint16 = 0x22
	OpSetBlanketTombstone uint16 = 0x23

	// Transaction operations
	OpBeginTransaction  uint16 = 0x30
	OpCommitTransaction uint16 = 0x31
	OpAbortTransaction  uint16 = 0x32

	// Maintenance operations
	OpFlush uint16 = 0x40

	// Layer operations
	OpDeleteLayer uint16 = 0x50

	// Response bit: set in op_code for response messages.
	ResponseBit uint16 = 0x8000
)

// RSI error codes returned in response status field.
const (
	StatusOK              uint32 = 0
	StatusNotFound        uint32 = 1
	StatusAlreadyExists   uint32 = 2
	StatusStorageError    uint32 = 3
	StatusNotEmpty        uint32 = 4
	StatusTooLarge        uint32 = 5
	StatusTxnBusy         uint32 = 6
	StatusInvalid         uint32 = 7
	StatusCASFailed       uint32 = 8
	StatusTxnNotSupported uint32 = 9
)

// RSI registration flags.
const (
	HivePrivate uint32 = 0x01
)

// Header sizes.
const (
	RequestHeaderSize  = 22 // total_len(4) + request_id(8) + op_code(2) + txn_id(8)
	ResponseHeaderSize = 14 // total_len(4) + request_id(8) + op_code(2)
)

// MaxRequestSize is the upper bound on a single RSI request message.
// Protects against resource exhaustion from malformed total_len.
// 16 MiB is generous for any legitimate request (the largest payload
// is RSI_SET_VALUE with a value body; LCS caps value data well below this).
const MaxRequestSize = 16 * 1024 * 1024

// RSI_WRITE_KEY field mask bits.
const (
	WriteKeyFieldSD            uint32 = 0x01
	WriteKeyFieldLastWriteTime uint32 = 0x02
)

// Path entry target types.
const (
	TargetGUID   uint8 = 0
	TargetHidden uint8 = 1
)

// RSI_BEGIN_TRANSACTION mode (PSD-005 §7.2, §11.3).
const (
	TxnReadWrite uint32 = 0 // read-write: read-your-own-writes + atomic commit
	TxnReadOnly  uint32 = 1 // point-in-time read-only snapshot (REG_IOC_BACKUP)
)
