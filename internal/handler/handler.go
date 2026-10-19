// Package handler implements RSI operation handlers for loregd.
//
// Each handler takes a parsed RSI request and executes the
// corresponding SQL operations against the appropriate hive database.
package handler

import (
	"sync"
	"time"

	"github.com/peios/loregd/internal/fold"
	"github.com/peios/loregd/internal/hivedb"
	"github.com/peios/loregd/internal/rsi"
)

// Handler routes RSI operations to the correct hive database.
type Handler struct {
	hives         map[string]*hivedb.HiveDB // folded hive name -> DB
	guidCache     sync.Map                  // rsi.GUID -> *hivedb.HiveDB
	requestBudget time.Duration
	txns          *txnManager
}

// New creates a Handler with the given hive databases.
// Seeds the GUID-to-hive cache with each hive's root GUID.
func New(hives []*hivedb.HiveDB) *Handler {
	h := &Handler{
		hives:         make(map[string]*hivedb.HiveDB, len(hives)),
		txns:          newTxnManager(),
		requestBudget: requestTimeout,
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

// Close releases transactions after the request loop has drained.
func (h *Handler) Close() { h.txns.close() }
