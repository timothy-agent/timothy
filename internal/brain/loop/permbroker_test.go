package loop

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/session"
)

// fakePermRow is one fakePermStore row.
type fakePermRow struct {
	p          PendingPermission
	decision   string
	resolved   bool
	carry      bool
	resolvedAt time.Time
}

// fakePermStore mirrors PGPermStore's SQL semantics in memory.
type fakePermStore struct {
	mu        sync.Mutex
	rows      map[string]*fakePermRow
	order     []string
	insertErr error
	inserted  []string
}

func newFakePermStore() *fakePermStore { return &fakePermStore{rows: map[string]*fakePermRow{}} }

func sameCall(a, b PendingPermission) bool {
	return a.SessionID == b.SessionID && a.MissionID == b.MissionID && a.Tool == b.Tool && string(a.Args) == string(b.Args)
}

func (f *fakePermStore) Insert(_ context.Context, p PendingPermission) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return f.insertErr
	}
	f.rows[p.ID] = &fakePermRow{p: p}
	f.order = append(f.order, p.ID)
	f.inserted = append(f.inserted, p.ID)
	return nil
}

func (f *fakePermStore) Redeem(_ context.Context, p PendingPermission, within time.Duration) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range f.order {
		r := f.rows[id]
		if r.carry && sameCall(r.p, p) && time.Since(r.resolvedAt) < within {
			r.carry = false
			return id, r.decision, nil
		}
	}
	return "", "", nil
}

func (f *fakePermStore) Adopt(_ context.Context, p PendingPermission, live []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	isLive := map[string]bool{}
	for _, id := range live {
		isLive[id] = true
	}
	for _, id := range f.order {
		r := f.rows[id]
		if !r.resolved && !isLive[id] && sameCall(r.p, p) {
			return id, nil
		}
	}
	return "", nil
}

func (f *fakePermStore) Resolve(_ context.Context, id, decision string, carry bool) (PendingPermission, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok || r.resolved {
		return PendingPermission{}, false, nil
	}
	r.resolved, r.decision, r.carry, r.resolvedAt = true, decision, carry, time.Now()
	return r.p, true, nil
}

func (f *fakePermStore) Expire(_ context.Context, live []string, _ time.Duration) (int64, error) {
	return 0, nil
}

func (f *fakePermStore) Pending(context.Context) ([]PendingPermission, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []PendingPermission
	for _, id := range f.order {
		if !f.rows[id].resolved {
			out = append(out, f.rows[id].p)
		}
	}
	return out, nil
}

func (f *fakePermStore) row(id string) fakePermRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rows[id]; ok {
		return *r
	}
	return fakePermRow{}
}

type recordingEvents struct {
	mu     sync.Mutex
	events []string
}

func (e *recordingEvents) Append(_ context.Context, sessionID, kind string, payload any) (int64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	b, _ := json.Marshal(payload)
	e.events = append(e.events, sessionID+"|"+kind+"|"+string(b))
	return 1, nil
}

func chatPrompt() PendingPermission {
	return PendingPermission{SessionID: "s1", Tool: "shell", Args: json.RawMessage(`{"command":"ls"}`), Danger: "safe", Rationale: "r", OriginKind: PermOriginChat}
}

func TestPermBrokerCreateWritesRowBeforeReturn(t *testing.T) {
	store := newFakePermStore()
	b := NewPermBroker()
	b.SetStore(store, nil, nil)
	id, ch, err := b.Create(t.Context(), chatPrompt())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ch == nil || len(id) != 32 {
		t.Fatalf("Create returned id=%q ch=%v, want 32-hex id and a channel", id, ch)
	}
	r := store.row(id)
	if r.p.ID != id || r.resolved || r.p.Tool != "shell" || r.p.OriginKind != PermOriginChat {
		t.Fatalf("row after Create = %+v, want pending shell chat row", r)
	}
}

func TestPermBrokerCreateNormalizesArgs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", `{}`},
		{`{"a":1}`, `{"a":1}`},
		{`{broken`, `"{broken"`},
	}
	for _, tc := range cases {
		store := newFakePermStore()
		b := NewPermBroker()
		b.SetStore(store, nil, nil)
		p := chatPrompt()
		p.Args = json.RawMessage(tc.in)
		id, _, err := b.Create(t.Context(), p)
		if err != nil {
			t.Fatalf("Create(%q): %v", tc.in, err)
		}
		if got := string(store.row(id).p.Args); got != tc.want {
			t.Fatalf("args %q stored as %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPermBrokerCreateInsertErrorLeavesNoWaiter(t *testing.T) {
	store := newFakePermStore()
	store.insertErr = errors.New("db down")
	b := NewPermBroker()
	b.SetStore(store, nil, nil)
	if _, _, err := b.Create(t.Context(), chatPrompt()); err == nil {
		t.Fatal("Create succeeded, want the insert error")
	}
	if ids := b.liveIDs(); len(ids) != 0 {
		t.Fatalf("live ids after failed Create = %v, want none", ids)
	}
}

func TestPermBrokerResolveMarksAndDelivers(t *testing.T) {
	store := newFakePermStore()
	events := &recordingEvents{}
	b := NewPermBroker()
	b.SetStore(store, events, nil)
	id, ch, _ := b.Create(t.Context(), chatPrompt())
	if !b.Resolve(t.Context(), id, DecideOnce) {
		t.Fatal("Resolve = false, want true for a live prompt")
	}
	if got := <-ch; got != DecideOnce {
		t.Fatalf("delivered %q, want once", got)
	}
	r := store.row(id)
	if !r.resolved || r.decision != DecideOnce || r.carry {
		t.Fatalf("row = %+v, want resolved once without carry-over (a live turn consumed it)", r)
	}
	if len(events.events) != 0 {
		t.Fatalf("session events = %v, want none: the live turn emits its own", events.events)
	}
}

func TestPermBrokerSecondResolveIsNoOp(t *testing.T) {
	store := newFakePermStore()
	b := NewPermBroker()
	b.SetStore(store, nil, nil)
	id, _, _ := b.Create(t.Context(), chatPrompt())
	b.Resolve(t.Context(), id, DecideDeny)
	if b.Resolve(t.Context(), id, DecideOnce) {
		t.Fatal("second Resolve = true, want false")
	}
	if r := store.row(id); r.decision != DecideDeny {
		t.Fatalf("decision after second Resolve = %q, want deny unchanged", r.decision)
	}
	if b.Resolve(t.Context(), "unknown", DecideOnce) {
		t.Fatal("Resolve of unknown id = true, want false")
	}
}

func TestPermBrokerForgetMarksTimeoutOnlyWhenPending(t *testing.T) {
	store := newFakePermStore()
	b := NewPermBroker()
	b.SetStore(store, nil, nil)
	id, _, _ := b.Create(t.Context(), chatPrompt())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := b.Forget(ctx, id); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if r := store.row(id); r.decision != DecideTimeout {
		t.Fatalf("decision after Forget = %q, want timeout", r.decision)
	}

	id2, _, _ := b.Create(t.Context(), PendingPermission{SessionID: "s2", Tool: "shell"})
	b.Resolve(t.Context(), id2, DecideSession)
	_ = b.Forget(t.Context(), id2)
	if r := store.row(id2); r.decision != DecideSession {
		t.Fatalf("decision after Forget of an answered prompt = %q, want session", r.decision)
	}
}

// TestPermBrokerResolveAfterRestartCarriesOver covers D-118: a new
// broker over the same store (the old one discarded, as on restart)
// resolves the old id, records a chat session event, and the next ask
// of the same call redeems the answer exactly once.
func TestPermBrokerResolveAfterRestartCarriesOver(t *testing.T) {
	store := newFakePermStore()
	old := NewPermBroker()
	old.SetStore(store, nil, nil)
	id, _, _ := old.Create(t.Context(), chatPrompt())

	events := &recordingEvents{}
	b := NewPermBroker()
	b.SetStore(store, events, nil)
	if !b.Resolve(t.Context(), id, DecideOnce) {
		t.Fatal("Resolve after restart = false, want true for a pending row")
	}
	if r := store.row(id); !r.resolved || r.decision != DecideOnce || !r.carry {
		t.Fatalf("row = %+v, want resolved once with carry-over", r)
	}
	want := "s1|" + session.KindPermissionResolved + `|{"id":"` + id + `","decision":"once"}`
	if len(events.events) != 1 || events.events[0] != want {
		t.Fatalf("session events = %v, want [%s]", events.events, want)
	}

	gotID, ch, err := b.Create(t.Context(), chatPrompt())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotID != id {
		t.Fatalf("redeemed id = %q, want %q", gotID, id)
	}
	select {
	case d := <-ch:
		if d != DecideOnce {
			t.Fatalf("redeemed decision = %q, want once", d)
		}
	default:
		t.Fatal("redeemed channel empty, want the carried-over decision")
	}
	nextID, next, _ := b.Create(t.Context(), chatPrompt())
	if nextID == id {
		t.Fatal("once answer redeemed twice")
	}
	select {
	case d := <-next:
		t.Fatalf("second ask got %q, want a fresh pending prompt", d)
	default:
	}
}

func TestPermBrokerDenyAfterRestartDoesNotCarry(t *testing.T) {
	store := newFakePermStore()
	old := NewPermBroker()
	old.SetStore(store, nil, nil)
	id, _, _ := old.Create(t.Context(), PendingPermission{SessionID: "s1", MissionID: "m1", Tool: "shell", OriginKind: PermOriginMission})

	events := &recordingEvents{}
	b := NewPermBroker()
	b.SetStore(store, events, nil)
	if !b.Resolve(t.Context(), id, DecideDeny) {
		t.Fatal("Resolve = false, want true")
	}
	if r := store.row(id); r.carry || r.decision != DecideDeny {
		t.Fatalf("row = %+v, want deny without carry-over", r)
	}
	if len(events.events) != 0 {
		t.Fatalf("session events = %v, want none for a mission row", events.events)
	}
}

func TestPermBrokerAdoptsOrphanedPrompt(t *testing.T) {
	store := newFakePermStore()
	old := NewPermBroker()
	old.SetStore(store, nil, nil)
	p := PendingPermission{SessionID: "s1", MissionID: "m1", Tool: "shell", Args: json.RawMessage(`{"command":"make"}`), OriginKind: PermOriginMission}
	id, _, _ := old.Create(t.Context(), p)

	b := NewPermBroker()
	b.SetStore(store, nil, nil)
	gotID, ch, err := b.Create(t.Context(), p)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotID != id {
		t.Fatalf("Create after restart id = %q, want the adopted %q", gotID, id)
	}
	if len(store.inserted) != 1 {
		t.Fatalf("inserted = %v, want no second row", store.inserted)
	}
	if !b.Resolve(t.Context(), id, DecideOnce) || <-ch != DecideOnce {
		t.Fatal("adopted prompt did not deliver live")
	}
	// A second concurrent ask of the same call gets its own row: the
	// adopted id already has a live waiter.
	id3, _, _ := b.Create(t.Context(), p)
	id4, _, _ := b.Create(t.Context(), p)
	if id3 == id4 {
		t.Fatal("two live asks share one id")
	}
}

func TestPermBrokerNilStoreKeepsInMemoryBehaviour(t *testing.T) {
	b := NewPermBroker()
	id, ch, err := b.Create(t.Context(), chatPrompt())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !b.Resolve(t.Context(), id, DecideSession) || <-ch != DecideSession {
		t.Fatal("nil-store Resolve did not deliver")
	}
	if b.Resolve(t.Context(), id, DecideSession) {
		t.Fatal("nil-store second Resolve = true, want false")
	}
	id2, _, _ := b.Create(t.Context(), chatPrompt())
	if err := b.Forget(t.Context(), id2); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if b.Resolve(t.Context(), id2, DecideOnce) {
		t.Fatal("Resolve after Forget = true, want false")
	}
	if p, err := b.Pending(t.Context()); p != nil || err != nil {
		t.Fatalf("nil-store Pending = %v, %v; want nil, nil", p, err)
	}
	if n, err := b.ExpireStale(t.Context()); n != 0 || err != nil {
		t.Fatalf("nil-store ExpireStale = %d, %v", n, err)
	}
}
