package hivedb

import (
	"context"
	"sync"
)

// Named access modes keep Acquire calls explicit without changing its boolean API.
const (
	ReadAccess  = false
	WriteAccess = true
)

// Gate excludes publication (including non-transactional writes) from reads.
// It is held for an RSI operation, never for the lifetime of a transaction.
// SQLite establishes snapshots independently for attached WAL databases; the
// gate lets a reader pin both sides before a commit can publish either side.
// Unlike sync.RWMutex, waiting here respects the request deadline.
type Gate struct {
	mu                      sync.Mutex
	readers, waitingWriters int
	writer                  bool
	changed                 chan struct{}
}

// notify must be called with mu held; it wakes every waiter to recheck eligibility.
func (g *Gate) notify() {
	if g.changed != nil {
		close(g.changed)
		g.changed = nil
	}
}

// Acquire obtains a read or write publication guard, waiting only until ctx expires.
// A successful caller must invoke the returned release function exactly once.
// Writers take priority over new readers, so sustained reads cannot starve publication.
func (g *Gate) Acquire(ctx context.Context, write bool) (func(), error) {
	g.mu.Lock()
	if write {
		g.waitingWriters++
	}
	for {
		if err := ctx.Err(); err != nil {
			if write {
				g.waitingWriters--
				g.notify()
			}
			g.mu.Unlock()
			return nil, err
		}
		if !g.writer && ((write && g.readers == 0) || (!write && g.waitingWriters == 0)) {
			if write {
				g.waitingWriters--
				g.writer = true
			} else {
				g.readers++
			}
			g.mu.Unlock()
			return func() {
				g.mu.Lock()
				if write {
					g.writer = false
				} else {
					g.readers--
				}
				g.notify()
				g.mu.Unlock()
			}, nil
		}
		if g.changed == nil {
			g.changed = make(chan struct{})
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		g.mu.Lock()
	}
}
