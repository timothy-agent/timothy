package chat

import (
	"sync"

	"github.com/SumonMSelim/timothy/internal/gateway/stream"
)

// broadcastBuf caps a subscriber's backlog of live turn events. A
// slow subscriber that fills this buffer is dropped rather than
// stalling the turn (same drop-on-full philosophy as missions.Hub) —
// losing the live tail is benign since a dropped subscriber's client
// falls back to a transcript refetch once the turn ends.
const broadcastBuf = 64

// turnBroadcaster is one turn's live event fan-out: every StreamEvent
// the relay forwards lands in buf (for late subscribers' replay) and
// is pushed to every live subscriber's channel. Presence of a
// broadcaster entry in Service.turns IS "this session has a turn in
// flight" (D-Option-B, see chat.go's turn lifecycle comment) — there
// is no separate busy-state registry.
type turnBroadcaster struct {
	mu   sync.Mutex
	buf  []stream.StreamEvent
	subs map[chan stream.StreamEvent]struct{}
}

func newTurnBroadcaster() *turnBroadcaster {
	return &turnBroadcaster{subs: map[chan stream.StreamEvent]struct{}{}}
}

// publish appends ev to the replay buffer and fans it out to every
// live subscriber. Non-blocking per subscriber: a full channel has ev
// dropped for that subscriber only, and the subscriber is then closed
// and unregistered — it will never see a gap-free stream again, so it
// is cut loose rather than left silently behind. The publisher (the
// turn's relay goroutine) never blocks on a slow watcher.
func (b *turnBroadcaster) publish(ev stream.StreamEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, ev)
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
			delete(b.subs, ch)
			close(ch)
		}
	}
}

// subscribe registers a new live subscriber and returns a snapshot of
// everything buffered so far plus the channel that carries every
// event published from this point on — the snapshot and the
// subscription share one lock acquisition, so no event can land in
// the gap between "read the buffer" and "start receiving live" (no
// duplication, no drop).
func (b *turnBroadcaster) subscribe() ([]stream.StreamEvent, <-chan stream.StreamEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	replay := append([]stream.StreamEvent(nil), b.buf...)
	ch := make(chan stream.StreamEvent, broadcastBuf)
	b.subs[ch] = struct{}{}
	return replay, ch
}

// closeAll closes and unregisters every live subscriber — called once
// the turn reaches its terminal persist, after which no further event
// will ever publish. Subsequent subscribe calls never happen because
// the broadcaster entry is removed from Service.turns in the same
// breath (see chat.go's runTurn/relay).
func (b *turnBroadcaster) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		close(ch)
	}
	b.subs = map[chan stream.StreamEvent]struct{}{}
}

// turnBroadcast registers a fresh broadcaster for sessionID, replacing
// any previous entry — a session can only have one turn in flight at a
// time in practice, but a stale entry (e.g. a prior turn whose
// terminal free lost a race) must never wedge turn_active permanently
// stuck true, so a new turn always wins the slot outright.
func (s *Service) turnBroadcast(sessionID string) *turnBroadcaster {
	b := newTurnBroadcaster()
	s.turnsMu.Lock()
	if s.turns == nil {
		s.turns = map[string]*turnBroadcaster{}
	}
	s.turns[sessionID] = b
	s.turnsMu.Unlock()
	return b
}

// turnDone frees sessionID's broadcaster entry and closes every live
// subscriber. Called after the turn's terminal persist is durable (see
// persistTurn) so turn_active flips false only once a refetch is
// guaranteed to see the completed turn — no window where the flag is
// down but the transcript is still stale.
func (s *Service) turnDone(sessionID string) {
	s.turnsMu.Lock()
	b := s.turns[sessionID]
	delete(s.turns, sessionID)
	s.turnsMu.Unlock()
	if b != nil {
		b.closeAll()
	}
}

// TurnActive reports whether sessionID currently has a turn in flight
// — the single source of truth GET /v1/sessions/{id} reads for
// turn_active, and the one Subscribe below reads to decide whether
// there is anything to replay.
func (s *Service) TurnActive(sessionID string) bool {
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	_, ok := s.turns[sessionID]
	return ok
}

// Subscribe attaches to sessionID's in-flight turn, if any: ok is
// false when no turn is currently running, in which case replay and
// live are both nil. Otherwise replay is every event buffered so far
// (send these to the client first, in order) and live is the channel
// to range over afterward until it closes (the turn's terminal, or
// this subscriber got dropped for being too slow — see publish).
func (s *Service) Subscribe(sessionID string) (replay []stream.StreamEvent, live <-chan stream.StreamEvent, ok bool) {
	s.turnsMu.Lock()
	b := s.turns[sessionID]
	s.turnsMu.Unlock()
	if b == nil {
		return nil, nil, false
	}
	replay, ch := b.subscribe()
	return replay, ch, true
}
