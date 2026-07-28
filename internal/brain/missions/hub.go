package missions

import (
	"context"
	"sync"
)

// Signal is a change hint pushed to subscribers — never a payload.
// Kind is "mission" (ID = mission id) or "notification" (ID =
// notification id). The client refetches the actual state over the
// normal REST endpoints; the hub only tells it something changed, so
// there's nothing here that can leak or go stale.
type Signal struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Hub is an in-process pub/sub fan-out from mission/notification
// writes to live SSE connections. It replaces the web's 5s poll: the
// web used to re-fetch missions/notifications on a timer regardless
// of whether anything changed; the hub instead pushes a hint the
// instant a write commits, and the client refetches only then.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Signal]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: map[chan Signal]struct{}{}}
}

// subSignalBuf caps a subscriber's backlog — signals carry no payload,
// so a client that misses one just refetches on the next signal or on
// reconnect; there is nothing to replay.
const subSignalBuf = 16

// Subscribe registers a new subscriber and returns its channel. The
// channel is closed and unregistered once ctx ends (e.g. the SSE
// request disconnects) — callers range over it until closed.
func (h *Hub) Subscribe(ctx context.Context) <-chan Signal {
	ch := make(chan Signal, subSignalBuf)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	go func() {
		<-ctx.Done()
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}()
	return ch
}

// Publish fans a signal out to every live subscriber. Non-blocking: a
// full (slow) subscriber has this signal dropped rather than stalling
// the writer that just committed a mission/notification change —
// losing a signal is benign since the client's own state always lives
// in Postgres, not in the hub.
func (h *Hub) Publish(sig Signal) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- sig:
		default:
		}
	}
}
