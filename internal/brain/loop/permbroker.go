package loop

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// Permission decisions a user can return for a parked tool call.
const (
	DecideOnce    = "once"
	DecideSession = "session"
	DecideDeny    = "deny"
)

// PermBroker connects parked tool calls to their answers: the loop
// registers a pending id and blocks on its channel; the permissions
// API resolves it. Everything is in-memory — a brain restart drops
// pending prompts, which the 10-minute deny timeout already treats as
// a deny.
type PermBroker struct {
	mu      sync.Mutex
	pending map[string]chan string
}

func NewPermBroker() *PermBroker {
	return &PermBroker{pending: map[string]chan string{}}
}

// Create registers a new pending prompt and returns its id and answer
// channel (buffered: resolving never blocks the API handler).
func (b *PermBroker) Create() (string, <-chan string) {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	id := hex.EncodeToString(buf)
	ch := make(chan string, 1)
	b.mu.Lock()
	b.pending[id] = ch
	b.mu.Unlock()
	return id, ch
}

// Resolve delivers the user's decision. False = unknown or already
// answered id (the API returns 404).
func (b *PermBroker) Resolve(id, decision string) bool {
	b.mu.Lock()
	ch, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	ch <- decision
	return true
}

// Forget drops a pending id after a timeout or cancellation.
func (b *PermBroker) Forget(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}
