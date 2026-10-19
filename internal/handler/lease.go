package handler

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/peios/loregd/internal/rsi"
)

// Renew before the lease closes, while checking ownership often enough to
// reclaim abandoned connections independently of normal request slots.
const (
	leaseMaintenanceInterval = 100 * time.Millisecond
	leaseRenewalWindow       = 3 * time.Second
)

var errExpiredTxn = errors.New("transaction lease expired")

// Called with manager.mu. Once terminal, a delayed renewal cannot resurrect it.
func (info *txnInfo) leaseExpired(now uint64) bool {
	if info.session != 0 && (now >= info.leaseNS || (info.lifetimeNS != 0 && now >= info.lifetimeNS)) {
		info.expired = true
	}
	return info.expired
}

func (m *txnManager) beginLeased(id uint64, readOnly bool, header rsi.RequestHeader) error {
	if header.Session == 0 {
		return m.beginMode(id, readOnly)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.txns[id]; exists {
		return fmt.Errorf("transaction %d already active", id)
	}
	info := &txnInfo{readOnly: readOnly, session: header.Session, leaseNS: header.LeaseNS, lifetimeNS: header.LifetimeNS}
	if info.leaseExpired(rsi.MonotonicNS()) || rsi.MonotonicNS() >= header.DeadlineNS {
		return errExpiredTxn
	}
	m.txns[id] = info
	return nil
}

// EnableLeases starts the control loop before dispatch. Renewal is independent
// of SQL and normal request slots. Cleanup takes the transaction operation gate;
// it therefore waits for an admitted COMMIT's actual outcome before rollback.
func (h *Handler) EnableLeases(renew func(session, id uint64) (uint64, error)) {
	m := h.txns
	m.stop = make(chan struct{})
	m.done = make(chan struct{})
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(leaseMaintenanceInterval)
		defer ticker.Stop()
		var cleanup sync.WaitGroup
		defer cleanup.Wait()
		for {
			select {
			case <-m.stop:
				return
			case <-ticker.C:
				m.maintainLeases(renew, &cleanup)
			}
		}
	}()
}

func (m *txnManager) maintainLeases(renew func(session, id uint64) (uint64, error), cleanup *sync.WaitGroup) {
	m.mu.Lock()
	pending := make(map[uint64]*txnInfo)
	now := rsi.MonotonicNS()
	for id, info := range m.txns {
		if info.session == 0 {
			continue
		}
		if info.leaseExpired(now) {
			if !info.cleanupStarted {
				info.cleanupStarted = true
				cleanup.Add(1)
				go func(id uint64) {
					defer cleanup.Done()
					m.abort(id)
				}(id)
			}
		} else if info.leaseNS-now <= uint64(leaseRenewalWindow) {
			pending[id] = info
		}
	}
	m.mu.Unlock()
	for id, info := range pending {
		deadline, err := renew(info.session, id)
		m.mu.Lock()
		if m.txns[id] == info && !info.leaseExpired(rsi.MonotonicNS()) {
			if err == nil && deadline > rsi.MonotonicNS() {
				info.leaseNS = deadline
			} else {
				// Ownership loss is terminal. An admitted commit still owns its gate
				// and must resolve; no later operation or renewal can enter.
				info.expired = true
			}
		}
		m.mu.Unlock()
	}
}
