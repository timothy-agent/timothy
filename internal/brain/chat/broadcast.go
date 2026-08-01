package chat

import (
	"context"
	"errors"
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
	mu     sync.Mutex
	buf    []stream.StreamEvent
	subs   map[chan stream.StreamEvent]struct{}
	cancel context.CancelFunc // set by Chat/Retry once the turn's detached ctx exists; nil until then
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

// setCancel stores the detached turn context's cancel func — called by
// Chat/Retry right after turnBegin returns, before runTurn does
// anything else, so StopTurn can never observe a nil cancel for a turn
// that's actually in flight (turnBegin already made it visible in
// Service.turns). Guarded by the same mutex as publish/subscribe so a
// racing StopTurn either sees it set or sees the broadcaster before
// registration completes at all (turnBegin's own lock covers that
// earlier race).
func (b *turnBroadcaster) setCancel(fn context.CancelFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cancel = fn
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

// ErrTurnInFlight marks a Chat or Retry call that lost the race to
// register sessionID's turn: another turn is already live, so this
// caller must not append anything to the session's event log (D-042,
// see turnBegin).
var ErrTurnInFlight = errors.New("turn already in flight")

// turnBegin is the sole entry point onto a session's turn slot:
// exclusive, atomic registration under turnsMu (D-042). A session can
// only have one turn in flight; unlike the old turnBroadcast (which
// always installed a fresh entry, unconditionally evicting whatever
// was there), this returns ErrTurnInFlight when an entry already
// exists, so a losing Chat/Retry call can bail out before appending
// even a user_message — the append-only log must never see the loser's
// write. Callers MUST call this before the turn's first event append;
// see Chat and Retry.
func (s *Service) turnBegin(sessionID string) (*turnBroadcaster, error) {
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	if s.turns == nil {
		s.turns = map[string]*turnBroadcaster{}
	}
	if _, exists := s.turns[sessionID]; exists {
		return nil, ErrTurnInFlight
	}
	b := newTurnBroadcaster()
	s.turns[sessionID] = b
	return b, nil
}

// turnDone frees sessionID's broadcaster entry and closes every live
// subscriber. Called after the turn's terminal persist is durable (see
// persistTurn) so turn_active flips false only once a refetch is
// guaranteed to see the completed turn — no window where the flag is
// down but the transcript is still stale. Also cancels the turn's
// detached context (if a cancel was ever stored) so it never leaks past
// the turn's own lifetime — every path here runs after the turn is
// truly over, so cancelling now is always safe, never premature.
func (s *Service) turnDone(sessionID string) {
	s.turnsMu.Lock()
	b := s.turns[sessionID]
	delete(s.turns, sessionID)
	s.turnsMu.Unlock()
	if b != nil {
		b.mu.Lock()
		cancel := b.cancel
		b.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		b.closeAll()
	}
}

// StopTurn cancels sessionID's in-flight turn, if any, and reports
// whether one was actually live. Cancelling the detached turn context
// makes the tool loop/gateway wind down (same path a real deadline or a
// brain shutdown already takes); the existing abnormal-end machinery in
// persistTurn then takes over — pending_state when partial text exists,
// turn_failed otherwise — and drainAndPersist runs turnDone exactly as
// on any other exit. StopTurn does NOT remove the Service.turns entry
// itself: the relay goroutine still owns that (via turnDone), so a
// caller polling TurnActive right after StopTurn can still see true
// until the wind-down actually completes.
func (s *Service) StopTurn(sessionID string) bool {
	s.turnsMu.Lock()
	b := s.turns[sessionID]
	s.turnsMu.Unlock()
	if b == nil {
		return false
	}
	b.mu.Lock()
	cancel := b.cancel
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return true
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

// ActiveSessions returns the IDs of every session with a turn
// currently in flight — the same Service.turns map TurnActive reads,
// snapshotted. Used to scope the pending-permissions query to live
// turns only: a permission_request whose turn already died (crash,
// abandoned) must not show as pending forever, so the caller queries
// session_events for just these IDs rather than the whole table.
func (s *Service) ActiveSessions() []string {
	s.turnsMu.Lock()
	defer s.turnsMu.Unlock()
	if len(s.turns) == 0 {
		return nil
	}
	ids := make([]string, 0, len(s.turns))
	for id := range s.turns {
		ids = append(ids, id)
	}
	return ids
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
