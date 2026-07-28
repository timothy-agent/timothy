package loop

import (
	"context"
	"sync"
)

// askGate serializes DecisionAsk handling per (session, tool, subject)
// key: a step's parallel same-tool calls (executeAll, bounded by
// errgroup) each hit a.perms.Resolve before any of them has answered
// the prompt, so all N would independently park on askUser — the
// first "session" answer grants a standing permission that covers the
// rest, but by then they're already waiting on their own prompts. The
// gate makes callers queue on the same key instead: only the first one
// through actually asks: the rest re-resolve once they get the lock,
// see the grant the first caller just recorded, and skip the prompt
// entirely.
type askGate struct {
	mu      sync.Mutex
	entries map[string]*gateEntry
}

// gateEntry is one key's lock: ch is a size-1 buffered channel used as
// a mutex token (send to acquire, receive to release), refs counts
// callers currently holding or waiting for it so the entry can be
// removed once nobody needs it — otherwise the map would grow for
// every distinct (session, tool, subject) ever seen and never shrink.
type gateEntry struct {
	ch   chan struct{}
	refs int
}

func newAskGate() *askGate {
	return &askGate{entries: map[string]*gateEntry{}}
}

// lock acquires the gate for key, blocking until it's free or ctx is
// done. release must be called exactly once to hand the lock to the
// next waiter (or, if there is none, to drop the entry). ok is false
// only when ctx ended before the lock was acquired; the caller must
// not call release in that case.
func (g *askGate) lock(ctx context.Context, key string) (release func(), ok bool) {
	g.mu.Lock()
	e, exists := g.entries[key]
	if !exists {
		e = &gateEntry{ch: make(chan struct{}, 1)}
		e.ch <- struct{}{}
		g.entries[key] = e
	}
	e.refs++
	g.mu.Unlock()

	select {
	case <-e.ch:
		return func() { g.release(key, e) }, true
	case <-ctx.Done():
		g.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(g.entries, key)
		}
		g.mu.Unlock()
		return nil, false
	}
}

// release returns e's token and drops the entry once nobody else
// holds or awaits it.
func (g *askGate) release(key string, e *gateEntry) {
	g.mu.Lock()
	e.refs--
	if e.refs == 0 {
		delete(g.entries, key)
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()
	e.ch <- struct{}{}
}
