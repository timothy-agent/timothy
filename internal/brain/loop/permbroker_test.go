package loop

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

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
	// afterAdopt runs after Adopt returns id, outside the lock.
	afterAdopt func(id string)
	// jsonbErrs makes the next Redeem/Insert call per key ("redeem",
	// "insert") fail once with a jsonb cast error.
	jsonbErrs map[string]bool
	pruned    []time.Duration
}

func newFakePermStore() *fakePermStore { return &fakePermStore{rows: map[string]*fakePermRow{}} }

func sameCall(a, b PendingPermission) bool {
	return a.SessionID == b.SessionID && a.MissionID == b.MissionID && a.Tool == b.Tool && string(a.Args) == string(b.Args)
}

// failJSONB consumes a queued jsonb cast failure for op.
func (f *fakePermStore) failJSONB(op string) error {
	if f.jsonbErrs[op] {
		delete(f.jsonbErrs, op)
		return &pgconn.PgError{Code: "22P05", Message: "unsupported Unicode escape sequence"}
	}
	return nil
}

func (f *fakePermStore) Insert(_ context.Context, p PendingPermission) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return f.insertErr
	}
	if err := f.failJSONB("insert"); err != nil {
		return err
	}
	f.rows[p.ID] = &fakePermRow{p: p}
	f.order = append(f.order, p.ID)
	f.inserted = append(f.inserted, p.ID)
	return nil
}

func (f *fakePermStore) Redeem(_ context.Context, p PendingPermission, onceWithin, sessionWithin time.Duration) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failJSONB("redeem"); err != nil {
		return "", "", err
	}
	for _, id := range f.order {
		r := f.rows[id]
		within := sessionWithin
		if r.decision == DecideOnce {
			within = onceWithin
		}
		if r.carry && sameCall(r.p, p) && time.Since(r.resolvedAt) < within {
			r.carry = false
			return id, r.decision, nil
		}
	}
	return "", "", nil
}

func (f *fakePermStore) RedeemID(_ context.Context, id string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return "", false, nil
	}
	if r.carry {
		r.carry = false
		return r.decision, !r.resolved, nil
	}
	return "", !r.resolved, nil
}

func (f *fakePermStore) Adopt(_ context.Context, p PendingPermission, live []string) (string, error) {
	id, err := f.adopt(p, live)
	if id != "" && f.afterAdopt != nil {
		f.afterAdopt(id)
	}
	return id, err
}

func (f *fakePermStore) adopt(p PendingPermission, live []string) (string, error) {
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

func (f *fakePermStore) Prune(_ context.Context, olderThan time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruned = append(f.pruned, olderThan)
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

func TestPermBrokerGet(t *testing.T) {
	if _, ok, err := NewPermBroker().Get(t.Context(), "x"); ok || err != nil {
		t.Fatalf("Get without a store = %v %v, want not found", ok, err)
	}
	store := newFakePermStore()
	b := NewPermBroker()
	b.SetStore(store, nil, nil)
	id, _, _ := b.Create(t.Context(), chatPrompt())
	p, ok, err := b.Get(t.Context(), id)
	if err != nil || !ok || p.ID != id || p.SessionID != chatPrompt().SessionID {
		t.Fatalf("Get pending = %+v %v %v", p, ok, err)
	}
	b.Resolve(t.Context(), id, DecideOnce)
	if _, ok, _ := b.Get(t.Context(), id); ok {
		t.Fatal("Get returned an answered prompt")
	}
	if _, ok, _ := b.Get(t.Context(), "unknown"); ok {
		t.Fatal("Get returned an unknown id")
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

// TestPermBrokerAnswerBetweenAdoptAndRegisterDeliversOnce covers the
// adopt/register race: the operator answers the orphan after Adopt
// returns it but before the new waiter registers. The answer is
// carried, then consumed by the adopting ask exactly once.
func TestPermBrokerAnswerBetweenAdoptAndRegisterDeliversOnce(t *testing.T) {
	store := newFakePermStore()
	old := NewPermBroker()
	old.SetStore(store, nil, nil)
	id, _, _ := old.Create(t.Context(), chatPrompt())

	b := NewPermBroker()
	b.SetStore(store, nil, nil)
	resolved := false
	store.afterAdopt = func(adopted string) {
		store.afterAdopt = nil
		resolved = b.Resolve(t.Context(), adopted, DecideOnce)
	}
	gotID, ch, err := b.Create(t.Context(), chatPrompt())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !resolved {
		t.Fatal("Resolve in the race window = false, want true")
	}
	if gotID != id || len(store.inserted) != 1 {
		t.Fatalf("id=%q inserted=%v, want the adopted %q and no new row", gotID, store.inserted, id)
	}
	if len(ch) != 1 || <-ch != DecideOnce {
		t.Fatal("waiter did not get the once answer")
	}
	if r := store.row(id); r.carry {
		t.Fatalf("row = %+v, want carry consumed", r)
	}
	if ids := b.liveIDs(); len(ids) != 0 {
		t.Fatalf("live ids = %v, want none after delivery", ids)
	}
	nextID, next, _ := b.Create(t.Context(), chatPrompt())
	if nextID == id || len(next) != 0 {
		t.Fatal("once answer delivered twice")
	}
}

// TestPermBrokerDenyBetweenAdoptAndRegisterOpensNewPrompt: a deny in
// the race window carries nothing, so the ask opens a fresh prompt, as
// it would had the deny landed before Adopt.
func TestPermBrokerDenyBetweenAdoptAndRegisterOpensNewPrompt(t *testing.T) {
	store := newFakePermStore()
	old := NewPermBroker()
	old.SetStore(store, nil, nil)
	id, _, _ := old.Create(t.Context(), chatPrompt())

	b := NewPermBroker()
	b.SetStore(store, nil, nil)
	store.afterAdopt = func(adopted string) {
		store.afterAdopt = nil
		b.Resolve(t.Context(), adopted, DecideDeny)
	}
	gotID, ch, err := b.Create(t.Context(), chatPrompt())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotID == id || len(store.inserted) != 2 || len(ch) != 0 {
		t.Fatalf("id=%q inserted=%v queued=%d, want a fresh pending prompt", gotID, store.inserted, len(ch))
	}
	if ids := b.liveIDs(); len(ids) != 1 || ids[0] != gotID {
		t.Fatalf("live ids = %v, want only %q", ids, gotID)
	}
}

func TestPermBrokerCarryWindowByDecision(t *testing.T) {
	cases := []struct {
		decision string
		age      time.Duration
		redeemed bool
	}{
		{DecideOnce, 29 * time.Minute, true},
		{DecideOnce, 31 * time.Minute, false},
		{DecideSession, 31 * time.Minute, true},
		{DecideSession, 11 * time.Hour, true},
		{DecideSession, 13 * time.Hour, false},
	}
	for _, tc := range cases {
		store := newFakePermStore()
		old := NewPermBroker()
		old.SetStore(store, nil, nil)
		id, _, _ := old.Create(t.Context(), chatPrompt())
		b := NewPermBroker()
		b.SetStore(store, nil, nil)
		b.Resolve(t.Context(), id, tc.decision)
		store.mu.Lock()
		store.rows[id].resolvedAt = time.Now().Add(-tc.age)
		store.mu.Unlock()
		gotID, ch, err := b.Create(t.Context(), chatPrompt())
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if got := gotID == id && len(ch) == 1; got != tc.redeemed {
			t.Fatalf("%s answered %v ago: redeemed=%v, want %v", tc.decision, tc.age, got, tc.redeemed)
		}
	}
}

func TestPermBrokerWrapsArgsJSONBRejects(t *testing.T) {
	for _, op := range []string{"redeem", "insert"} {
		store := newFakePermStore()
		store.jsonbErrs = map[string]bool{op: true}
		b := NewPermBroker()
		b.SetStore(store, nil, nil)
		p := chatPrompt()
		p.Args = json.RawMessage(`{"a":"\u0000"}`)
		id, ch, err := b.Create(t.Context(), p)
		if err != nil {
			t.Fatalf("%s: Create: %v", op, err)
		}
		want, _ := json.Marshal(`{"a":"\u0000"}`)
		if got := string(store.row(id).p.Args); got != string(want) {
			t.Fatalf("%s: args stored as %s, want %s", op, got, want)
		}
		if len(ch) != 0 {
			t.Fatalf("%s: want a pending prompt", op)
		}
	}
}

func TestPermBrokerOtherStoreErrorsNotRetried(t *testing.T) {
	store := newFakePermStore()
	store.insertErr = &pgconn.PgError{Code: "23505"}
	b := NewPermBroker()
	b.SetStore(store, nil, nil)
	if _, _, err := b.Create(t.Context(), chatPrompt()); err == nil {
		t.Fatal("Create succeeded, want the unique violation")
	}
}

func TestPermBrokerExpireStalePrunes(t *testing.T) {
	store := newFakePermStore()
	b := NewPermBroker()
	b.SetStore(store, nil, nil)
	if _, err := b.ExpireStale(t.Context()); err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	if len(store.pruned) != 1 || store.pruned[0] != permRetention {
		t.Fatalf("pruned = %v, want one prune at %v", store.pruned, permRetention)
	}
}

func TestPermBrokerRunExpiryTicksUntilCancel(t *testing.T) {
	store := newFakePermStore()
	b := NewPermBroker()
	b.SetStore(store, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		b.RunExpiry(ctx, time.Millisecond)
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for {
		store.mu.Lock()
		n := len(store.pruned)
		store.mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("RunExpiry did not tick")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
}
