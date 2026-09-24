package loop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/brain/session"
)

// Permission decisions a user can return for a parked tool call.
const (
	DecideOnce    = "once"
	DecideSession = "session"
	DecideDeny    = "deny"
	// DecideTimeout is never a user answer: askUser returns it when its
	// wait ends without one (the 10-minute chat timer, or the turn's own
	// context ending) — issue #650 split this out of DecideDeny so the
	// event log and the model stop being told the user denied a call
	// nobody was ever asked to decide.
	DecideTimeout = "timeout"
)

// Origin kinds a pending permission row records.
const (
	PermOriginChat    = "chat"
	PermOriginMission = "mission"
)

// PendingPermission is one persisted permission prompt: Create's input
// and a row of the pending listing.
type PendingPermission struct {
	ID           string          `json:"id"`
	SessionID    string          `json:"session_id"`
	SessionTitle string          `json:"session_title"`
	MissionID    string          `json:"mission_id"`
	Tool         string          `json:"tool"`
	Args         json.RawMessage `json:"args"`
	Danger       string          `json:"danger"`
	Rationale    string          `json:"rationale"`
	OriginKind   string          `json:"origin_kind"`
	RequestedAt  time.Time       `json:"requested_at"`
}

// PermStore persists pending prompts; PGPermStore is the production
// implementation, tests fake it.
type PermStore interface {
	// Insert writes a new pending row with p.ID.
	Insert(ctx context.Context, p PendingPermission) error
	// Redeem consumes a carried-over answer for the same call (session,
	// mission, tool, args) resolved within its window (onceWithin for a
	// once answer, sessionWithin for a session one); id is "" when none
	// exists.
	Redeem(ctx context.Context, p PendingPermission, onceWithin, sessionWithin time.Duration) (id, decision string, err error)
	// RedeemID consumes row id's carried-over answer, if any. decision
	// is "" when nothing was consumed; pending reports whether the row
	// is still unresolved.
	RedeemID(ctx context.Context, id string) (decision string, pending bool, err error)
	// Adopt returns the id of a still-pending row for the same call
	// whose id is not in live, "" when none exists.
	Adopt(ctx context.Context, p PendingPermission, live []string) (string, error)
	// Resolve marks a pending row resolved with decision; carry marks
	// the answer redeemable by the next matching ask. ok is false when
	// id is unknown or already resolved.
	Resolve(ctx context.Context, id, decision string, carry bool) (row PendingPermission, ok bool, err error)
	// Expire marks timeout on pending rows not in live that no turn can
	// still wait on: chat rows older than chatTimeout, mission rows over
	// a minute old the mission no longer references (the grace covers
	// the gap before SetPendingPermission lands).
	Expire(ctx context.Context, live []string, chatTimeout time.Duration) (int64, error)
	// Prune deletes rows resolved more than olderThan ago.
	Prune(ctx context.Context, olderThan time.Duration) (int64, error)
	// Pending lists every unresolved row, oldest first.
	Pending(ctx context.Context) ([]PendingPermission, error)
}

// PermBroker connects parked tool calls to their answers: the loop
// registers a pending id and blocks on its channel; the permissions
// API resolves it.
//
// D-118: prompts persist in pending_permissions when a store is set,
// replacing the memory-only broker. A prompt from a previous process
// stays answerable by id: an answer with no live waiter is recorded,
// and a once/session answer carries over to the next ask of the same
// call (same session, mission, tool and args), which consumes it
// exactly once; session then records its grant through the loop's own
// perms.Grant path. A deny is recorded only. A re-driven turn asking
// the same call adopts the old id instead of opening a second prompt.
// Without a store the broker is in-memory only.
type PermBroker struct {
	mu      sync.Mutex
	pending map[string]chan string
	// resolveMu serializes Resolve against Create's adopt path, so an
	// answer landing between Adopt and register is seen by exactly one
	// of them.
	resolveMu sync.Mutex
	store   PermStore
	events  EventAppender
	log     *slog.Logger
}

func NewPermBroker() *PermBroker {
	return &PermBroker{pending: map[string]chan string{}, log: slog.New(slog.DiscardHandler)}
}

// SetStore persists prompts in s; events receives the
// permission_resolved session event for a chat prompt answered with
// no live turn. events may be nil.
func (b *PermBroker) SetStore(s PermStore, events EventAppender, log *slog.Logger) {
	b.store = s
	b.events = events
	if log != nil {
		b.log = log
	}
}

// Create registers a new pending prompt and returns its id and answer
// channel (buffered: resolving never blocks the API handler). With a
// store, the row is durable before Create returns; a carried-over
// answer comes back already on the channel.
func (b *PermBroker) Create(ctx context.Context, p PendingPermission) (string, <-chan string, error) {
	ch := make(chan string, 1)
	if b.store == nil {
		id := newPermID()
		b.register(id, ch)
		return id, ch, nil
	}
	p.Args = normalizeArgs(p.Args)
	var id, decision string
	err := b.withArgsFallback(&p, func() error {
		var err error
		id, decision, err = b.store.Redeem(ctx, p, onceCarryTTL, sessionGrantTTL)
		return err
	})
	if err != nil {
		return "", nil, err
	}
	if id != "" {
		ch <- decision
		return id, ch, nil
	}
	adopted, err := b.store.Adopt(ctx, p, b.liveIDs())
	if err != nil {
		return "", nil, err
	}
	if adopted != "" {
		ok, err := b.adopt(ctx, adopted, ch)
		if err != nil {
			return "", nil, err
		}
		if ok {
			return adopted, ch, nil
		}
	}
	// Registered before the insert so Expire never sees the new row
	// without a live waiter.
	p.ID = newPermID()
	b.register(p.ID, ch)
	if err := b.withArgsFallback(&p, func() error { return b.store.Insert(ctx, p) }); err != nil {
		b.drop(p.ID)
		return "", nil, err
	}
	return p.ID, ch, nil
}

// adopt registers ch as id's waiter, then re-reads the row: an answer
// recorded after Adopt but before register is consumed and delivered
// here. False (registration dropped) when id already has a waiter or
// was resolved with nothing left to carry; the caller opens a new
// prompt.
func (b *PermBroker) adopt(ctx context.Context, id string, ch chan string) (bool, error) {
	b.resolveMu.Lock()
	defer b.resolveMu.Unlock()
	if !b.register(id, ch) {
		return false, nil
	}
	decision, pending, err := b.store.RedeemID(ctx, id)
	if err != nil {
		b.drop(id)
		return false, err
	}
	if decision != "" {
		b.drop(id)
		ch <- decision
		return true, nil
	}
	if !pending {
		b.drop(id)
		return false, nil
	}
	return true, nil
}

// Resolve delivers the user's decision: marks the row resolved and
// wakes the live waiter, if any. False = unknown or already answered
// id (the API returns 404).
func (b *PermBroker) Resolve(ctx context.Context, id, decision string) bool {
	b.resolveMu.Lock()
	defer b.resolveMu.Unlock()
	b.mu.Lock()
	ch, live := b.pending[id]
	if live {
		delete(b.pending, id)
	}
	b.mu.Unlock()
	stored := false
	if b.store != nil {
		carry := !live && (decision == DecideOnce || decision == DecideSession)
		row, ok, err := b.store.Resolve(ctx, id, decision, carry)
		if err != nil {
			b.log.Warn("permission broker: resolve row", "id", id, "error", err)
		}
		stored = ok
		if ok && !live && row.MissionID == "" && row.SessionID != "" && b.events != nil {
			if _, err := b.events.Append(ctx, row.SessionID, session.KindPermissionResolved,
				session.PermissionResolved{ID: id, Decision: decision}); err != nil {
				b.log.Warn("permission broker: append permission_resolved", "id", id, "error", err)
			}
		}
	}
	if live {
		ch <- decision
		return true
	}
	return stored
}

// Forget drops a pending id after a timeout or cancellation and marks
// its row timeout if still pending.
func (b *PermBroker) Forget(ctx context.Context, id string) error {
	b.drop(id)
	if b.store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	_, _, err := b.store.Resolve(ctx, id, DecideTimeout, false)
	return err
}

// Pending lists every unresolved persisted prompt; nil without a store.
func (b *PermBroker) Pending(ctx context.Context) ([]PendingPermission, error) {
	if b.store == nil {
		return nil, nil
	}
	return b.store.Pending(ctx)
}

// Get returns one unresolved persisted prompt; ok is false without a
// store or when id is unknown or already answered.
func (b *PermBroker) Get(ctx context.Context, id string) (PendingPermission, bool, error) {
	pending, err := b.Pending(ctx)
	if err != nil {
		return PendingPermission{}, false, err
	}
	for _, p := range pending {
		if p.ID == id {
			return p, true, nil
		}
	}
	return PendingPermission{}, false, nil
}

// ExpireStale marks timeout on persisted prompts no live turn waits on
// and no future ask can adopt (see PermStore.Expire), then deletes rows
// resolved more than permRetention ago; chat prompts use the loop's own
// permission timeout.
func (b *PermBroker) ExpireStale(ctx context.Context) (int64, error) {
	if b.store == nil {
		return 0, nil
	}
	n, err := b.store.Expire(ctx, b.liveIDs(), getPermissionTimeout())
	if err != nil {
		return n, err
	}
	if _, err := b.store.Prune(ctx, permRetention); err != nil {
		return n, err
	}
	return n, nil
}

// RunExpiry calls ExpireStale every interval until ctx ends. It runs
// whether or not missions are enabled (D-118).
func (b *PermBroker) RunExpiry(ctx context.Context, interval time.Duration) {
	if b.store == nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := b.ExpireStale(ctx); err != nil {
				b.log.Error("permission broker: expire stale prompts", "error", err)
			}
		}
	}
}

// register adds a live waiter for id; false when id already has one.
func (b *PermBroker) register(id string, ch chan string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.pending[id]; ok {
		return false
	}
	b.pending[id] = ch
	return true
}

func (b *PermBroker) drop(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

func (b *PermBroker) liveIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]string, 0, len(b.pending))
	for id := range b.pending {
		ids = append(ids, id)
	}
	return ids
}

func newPermID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// normalizeArgs makes args storable as jsonb: empty becomes {}, and
// input that is not valid JSON is stored as a JSON string.
func normalizeArgs(args json.RawMessage) json.RawMessage {
	if len(args) == 0 {
		return json.RawMessage(`{}`)
	}
	if json.Valid(args) {
		return args
	}
	s, _ := json.Marshal(string(args))
	return s
}

// withArgsFallback runs fn; when Postgres rejects p.Args as jsonb
// (valid JSON it cannot store, such as \u0000 or an out-of-range
// number), it wraps p.Args as a JSON string and runs fn once more.
func (b *PermBroker) withArgsFallback(p *PendingPermission, fn func() error) error {
	err := fn()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "22P02", "22P05", "22003":
	default:
		return err
	}
	s, _ := json.Marshal(string(p.Args))
	p.Args = s
	return fn()
}
