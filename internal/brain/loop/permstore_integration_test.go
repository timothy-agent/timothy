//go:build integration

package loop

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/session"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

func integrationPermPool(t *testing.T) *pgpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(t.Context(), dsn, log)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := pool.WaitHealthy(ctx); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := migrate.Run(ctx, db, migrations.FS, log); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// chatSession creates a session row, deleted (with its prompts, by
// cascade) at cleanup.
func chatSession(t *testing.T, pool *pgpool.Pool) string {
	t.Helper()
	db, _ := pool.Get()
	var id string
	if err := db.QueryRow(t.Context(), `INSERT INTO sessions (title) VALUES ('permstore itest') RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.Exec(ctx, `DELETE FROM session_events WHERE session_id = $1`, id)
		_, _ = db.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	})
	return id
}

func rowDecision(t *testing.T, pool *pgpool.Pool, id string) (decision string, carry bool) {
	t.Helper()
	db, _ := pool.Get()
	if err := db.QueryRow(t.Context(), `SELECT COALESCE(decision, ''), carry_over FROM pending_permissions WHERE id = $1`, id).Scan(&decision, &carry); err != nil {
		t.Fatalf("read row %s: %v", id, err)
	}
	return decision, carry
}

// TestChatPermissionAnsweredAfterRestartIsRecordedAndRedeemed covers
// the chat half of #820: a chat prompt from a discarded broker is
// listed by a new one, answered (row resolved with the decision, a
// permission_resolved session event appended), and the next ask of the
// same call in that session redeems it once instead of resolving as a
// timeout.
func TestChatPermissionAnsweredAfterRestartIsRecordedAndRedeemed(t *testing.T) {
	pool := integrationPermPool(t)
	ctx := t.Context()
	sid := chatSession(t, pool)
	sessions := session.NewStore(pool, slog.New(slog.DiscardHandler))
	p := PendingPermission{SessionID: sid, Tool: "shell", Args: json.RawMessage(`{"command":"make test"}`), Danger: "safe", Rationale: "r", OriginKind: PermOriginChat}

	old := NewPermBroker()
	old.SetStore(NewPGPermStore(pool), sessions, nil)
	id, _, err := old.Create(ctx, p)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	restarted := NewPermBroker()
	restarted.SetStore(NewPGPermStore(pool), sessions, nil)
	pending, err := restarted.Pending(ctx)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	var listed *PendingPermission
	for i := range pending {
		if pending[i].ID == id {
			listed = &pending[i]
		}
	}
	if listed == nil || listed.SessionTitle != "permstore itest" || listed.Danger != "safe" || listed.MissionID != "" ||
		listed.OriginKind != PermOriginChat || string(listed.Args) != `{"command": "make test"}` {
		t.Fatalf("listed = %+v, want the chat prompt with title, danger and args", listed)
	}

	if !restarted.Resolve(ctx, id, DecideOnce) {
		t.Fatal("Resolve after restart = false, want true")
	}
	if d, carry := rowDecision(t, pool, id); d != DecideOnce || !carry {
		t.Fatalf("row decision=%q carry=%v, want once carried over (not timeout)", d, carry)
	}
	events, err := sessions.Events(ctx, sid)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	found := false
	for _, ev := range events {
		if ev.Kind == session.KindPermissionResolved {
			var r session.PermissionResolved
			_ = json.Unmarshal(ev.Payload, &r)
			found = r.ID == id && r.Decision == DecideOnce
		}
	}
	if !found {
		t.Fatalf("session events = %+v, want a permission_resolved for %s", events, id)
	}

	gotID, ch, err := restarted.Create(ctx, p)
	if err != nil {
		t.Fatalf("Create (next turn): %v", err)
	}
	if gotID != id || <-ch != DecideOnce {
		t.Fatalf("next ask id = %q, want the redeemed %q with once", gotID, id)
	}
	if _, carry := rowDecision(t, pool, id); carry {
		t.Fatal("carry-over still set after redemption")
	}
	third, thirdCh, err := restarted.Create(ctx, p)
	if err != nil {
		t.Fatalf("Create (third): %v", err)
	}
	select {
	case d := <-thirdCh:
		t.Fatalf("third ask got %q, want a fresh pending prompt: once is single-use", d)
	default:
	}
	if err := restarted.Forget(ctx, third); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if d, _ := rowDecision(t, pool, third); d != DecideTimeout {
		t.Fatalf("forgotten prompt decision = %q, want timeout", d)
	}
	if restarted.Resolve(ctx, third, DecideOnce) {
		t.Fatal("Resolve of a timed-out prompt = true, want false")
	}
}

// TestChatPromptAdoptedByNextAskAfterRestart: a still-pending chat
// prompt from a discarded broker is re-offered under the same id when
// the next turn asks the same call, and answering it delivers live.
func TestChatPromptAdoptedByNextAskAfterRestart(t *testing.T) {
	pool := integrationPermPool(t)
	ctx := t.Context()
	sid := chatSession(t, pool)
	p := PendingPermission{SessionID: sid, Tool: "shell", Args: json.RawMessage(`{"command":"ls"}`), OriginKind: PermOriginChat}

	old := NewPermBroker()
	old.SetStore(NewPGPermStore(pool), nil, nil)
	id, _, err := old.Create(ctx, p)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	restarted := NewPermBroker()
	restarted.SetStore(NewPGPermStore(pool), nil, nil)
	gotID, ch, err := restarted.Create(ctx, p)
	if err != nil {
		t.Fatalf("Create after restart: %v", err)
	}
	if gotID != id {
		t.Fatalf("adopted id = %q, want %q", gotID, id)
	}
	if !restarted.Resolve(ctx, id, DecideDeny) || <-ch != DecideDeny {
		t.Fatal("adopted prompt did not deliver live")
	}
	if d, carry := rowDecision(t, pool, id); d != DecideDeny || carry {
		t.Fatalf("row decision=%q carry=%v, want deny without carry-over", d, carry)
	}
}

// missionRow creates a mission in sid, deleted (with its prompts, by
// cascade) at cleanup.
func missionRow(t *testing.T, pool *pgpool.Pool, sid string) string {
	t.Helper()
	db, _ := pool.Get()
	var id string
	if err := db.QueryRow(t.Context(), `INSERT INTO missions (goal, kind, origin_kind, session_id)
		VALUES ('permstore itest', 'general', 'api', $1) RETURNING id`, sid).Scan(&id); err != nil {
		t.Fatalf("insert mission: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = db.Exec(ctx, `DELETE FROM mission_events WHERE mission_id = $1`, id)
		_, _ = db.Exec(ctx, `DELETE FROM missions WHERE id = $1`, id)
	})
	return id
}

// carried writes p as a row resolved with decision and carry-over set.
func carried(t *testing.T, store *PGPermStore, p PendingPermission, decision string) string {
	t.Helper()
	p.ID = newPermID()
	if err := store.Insert(t.Context(), p); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, ok, err := store.Resolve(t.Context(), p.ID, decision, true); err != nil || !ok {
		t.Fatalf("Resolve: %v, %v", ok, err)
	}
	return p.ID
}

func backdate(t *testing.T, pool *pgpool.Pool, id, column string, by time.Duration) {
	t.Helper()
	db, _ := pool.Get()
	if _, err := db.Exec(t.Context(), `UPDATE pending_permissions SET `+column+` = now() - $2::interval WHERE id = $1`,
		id, pgInterval(by)); err != nil {
		t.Fatalf("backdate %s: %v", column, err)
	}
}

func rowExists(t *testing.T, pool *pgpool.Pool, id string) bool {
	t.Helper()
	db, _ := pool.Get()
	var n int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM pending_permissions WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", id, err)
	}
	return n == 1
}

// TestRedeemMatchesOnlyTheSameCall: a carried answer is redeemed only
// by the same session, mission, tool and args (jsonb equality, so key
// order does not matter) inside its window.
func TestRedeemMatchesOnlyTheSameCall(t *testing.T) {
	pool := integrationPermPool(t)
	ctx := t.Context()
	store := NewPGPermStore(pool)
	sid := chatSession(t, pool)
	otherSID := chatSession(t, pool)
	mid := missionRow(t, pool, sid)
	chat := PendingPermission{SessionID: sid, Tool: "shell", Args: json.RawMessage(`{"a":1,"b":"x"}`), OriginKind: PermOriginChat}
	mission := chat
	mission.MissionID, mission.OriginKind = mid, PermOriginMission

	redeem := func(p PendingPermission) string {
		t.Helper()
		id, _, err := store.Redeem(ctx, p, onceCarryTTL, sessionGrantTTL)
		if err != nil {
			t.Fatalf("Redeem: %v", err)
		}
		return id
	}

	chatID := carried(t, store, chat, DecideOnce)
	missionID := carried(t, store, mission, DecideOnce)
	otherArgs := chat
	otherArgs.Args = json.RawMessage(`{"a":2,"b":"x"}`)
	otherSession := chat
	otherSession.SessionID = otherSID
	otherTool := chat
	otherTool.Tool = "fetch"
	for name, p := range map[string]PendingPermission{"different args": otherArgs, "other session": otherSession, "other tool": otherTool} {
		if id := redeem(p); id != "" {
			t.Fatalf("%s redeemed %s, want refused", name, id)
		}
	}
	chatAsMission := mission
	chatAsMission.Tool = "chat-only"
	chatOnly := chat
	chatOnly.Tool = "chat-only"
	chatOnlyID := carried(t, store, chatOnly, DecideOnce)
	if id := redeem(chatAsMission); id != "" {
		t.Fatalf("mission ask redeemed chat row %s, want refused", id)
	}
	missionOnly := mission
	missionOnly.Tool = "mission-only"
	missionOnlyID := carried(t, store, missionOnly, DecideOnce)
	missionAsChat := chat
	missionAsChat.Tool = "mission-only"
	if id := redeem(missionAsChat); id != "" {
		t.Fatalf("chat ask redeemed mission row %s, want refused", id)
	}

	reordered := chat
	reordered.Args = json.RawMessage(`{"b": "x", "a": 1}`)
	if id := redeem(reordered); id != chatID {
		t.Fatalf("reordered args redeemed %q, want %q", id, chatID)
	}
	if id := redeem(mission); id != missionID {
		t.Fatalf("mission ask redeemed %q, want %q", id, missionID)
	}
	if id := redeem(chatOnly); id != chatOnlyID {
		t.Fatalf("chat-only ask redeemed %q, want %q", id, chatOnlyID)
	}
	if id := redeem(missionOnly); id != missionOnlyID {
		t.Fatalf("mission-only ask redeemed %q, want %q", id, missionOnlyID)
	}
	if id := redeem(chat); id != "" {
		t.Fatalf("second redeem got %s, want single use", id)
	}
}

// TestRedeemWindowDependsOnDecision: once carries for onceCarryTTL,
// session for sessionGrantTTL.
func TestRedeemWindowDependsOnDecision(t *testing.T) {
	pool := integrationPermPool(t)
	ctx := t.Context()
	store := NewPGPermStore(pool)
	sid := chatSession(t, pool)
	cases := []struct {
		tool, decision string
		age            time.Duration
		redeemed       bool
	}{
		{"once-fresh", DecideOnce, 29 * time.Minute, true},
		{"once-stale", DecideOnce, 31 * time.Minute, false},
		{"session-fresh", DecideSession, 11 * time.Hour, true},
		{"session-stale", DecideSession, 13 * time.Hour, false},
	}
	for _, tc := range cases {
		p := PendingPermission{SessionID: sid, Tool: tc.tool, Args: json.RawMessage(`{}`), OriginKind: PermOriginChat}
		id := carried(t, store, p, tc.decision)
		backdate(t, pool, id, "resolved_at", tc.age)
		got, _, err := store.Redeem(ctx, p, onceCarryTTL, sessionGrantTTL)
		if err != nil {
			t.Fatalf("Redeem: %v", err)
		}
		if (got == id) != tc.redeemed {
			t.Fatalf("%s answered %v ago: redeemed=%v, want %v", tc.decision, tc.age, got == id, tc.redeemed)
		}
	}
}

// TestConcurrentRedeemIsExactlyOnce: parallel asks of one carried call
// consume it once.
func TestConcurrentRedeemIsExactlyOnce(t *testing.T) {
	pool := integrationPermPool(t)
	store := NewPGPermStore(pool)
	sid := chatSession(t, pool)
	p := PendingPermission{SessionID: sid, Tool: "shell", Args: json.RawMessage(`{"command":"ls"}`), OriginKind: PermOriginChat}
	id := carried(t, store, p, DecideSession)

	const n = 8
	var wg sync.WaitGroup
	hits := make(chan string, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, _, err := store.Redeem(t.Context(), p, onceCarryTTL, sessionGrantTTL)
			if err != nil {
				t.Errorf("Redeem: %v", err)
			}
			if got != "" {
				hits <- got
			}
		}()
	}
	wg.Wait()
	close(hits)
	var got []string
	for h := range hits {
		got = append(got, h)
	}
	if len(got) != 1 || got[0] != id {
		t.Fatalf("hits = %v, want exactly [%s]", got, id)
	}
}

// TestRedeemIDConsumesCarryOnce covers the adopt re-check against real
// Postgres.
func TestRedeemIDConsumesCarryOnce(t *testing.T) {
	pool := integrationPermPool(t)
	ctx := t.Context()
	store := NewPGPermStore(pool)
	sid := chatSession(t, pool)
	p := PendingPermission{SessionID: sid, Tool: "shell", Args: json.RawMessage(`{}`), OriginKind: PermOriginChat}
	id := carried(t, store, p, DecideOnce)
	if d, pending, err := store.RedeemID(ctx, id); err != nil || d != DecideOnce || pending {
		t.Fatalf("RedeemID = %q, %v, %v; want once, resolved", d, pending, err)
	}
	if d, pending, err := store.RedeemID(ctx, id); err != nil || d != "" || pending {
		t.Fatalf("second RedeemID = %q, %v, %v; want nothing", d, pending, err)
	}
	p.ID = newPermID()
	if err := store.Insert(ctx, p); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if d, pending, err := store.RedeemID(ctx, p.ID); err != nil || d != "" || !pending {
		t.Fatalf("RedeemID(pending) = %q, %v, %v; want still pending", d, pending, err)
	}
	if d, pending, err := store.RedeemID(ctx, "missing"); err != nil || d != "" || pending {
		t.Fatalf("RedeemID(missing) = %q, %v, %v", d, pending, err)
	}
}

// TestExpireSparesFreshMissionRows: a mission row inserted just before
// SetPendingPermission lands is not expired; the same row over a
// minute old is.
func TestExpireSparesFreshMissionRows(t *testing.T) {
	pool := integrationPermPool(t)
	ctx := t.Context()
	store := NewPGPermStore(pool)
	sid := chatSession(t, pool)
	mid := missionRow(t, pool, sid)
	p := PendingPermission{ID: newPermID(), SessionID: sid, MissionID: mid, Tool: "shell", Args: json.RawMessage(`{}`), OriginKind: PermOriginMission}
	if err := store.Insert(ctx, p); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := store.Expire(ctx, []string{}, time.Hour); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if d, _ := rowDecision(t, pool, p.ID); d != "" {
		t.Fatalf("fresh mission row decision = %q, want still pending", d)
	}
	backdate(t, pool, p.ID, "created_at", 2*time.Minute)
	if _, err := store.Expire(ctx, []string{}, time.Hour); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if d, _ := rowDecision(t, pool, p.ID); d != DecideTimeout {
		t.Fatalf("stale mission row decision = %q, want timeout", d)
	}
}

// TestExpireStaleDeletesOldResolvedRows covers retention: rows resolved
// over permRetention ago are deleted; newer and pending rows stay.
func TestExpireStaleDeletesOldResolvedRows(t *testing.T) {
	pool := integrationPermPool(t)
	ctx := t.Context()
	store := NewPGPermStore(pool)
	sid := chatSession(t, pool)
	mk := func(tool string) string {
		p := PendingPermission{ID: newPermID(), SessionID: sid, Tool: tool, Args: json.RawMessage(`{}`), OriginKind: PermOriginChat}
		if err := store.Insert(ctx, p); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		return p.ID
	}
	old, recent, pending := mk("old"), mk("recent"), mk("pending")
	for _, id := range []string{old, recent} {
		if _, ok, err := store.Resolve(ctx, id, DecideDeny, false); err != nil || !ok {
			t.Fatalf("Resolve: %v, %v", ok, err)
		}
	}
	backdate(t, pool, old, "resolved_at", permRetention+time.Hour)
	backdate(t, pool, recent, "resolved_at", permRetention-time.Hour)

	b := NewPermBroker()
	b.SetStore(store, nil, nil)
	if _, err := b.ExpireStale(ctx); err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}
	if rowExists(t, pool, old) {
		t.Fatal("row resolved past retention still present")
	}
	if !rowExists(t, pool, recent) || !rowExists(t, pool, pending) {
		t.Fatal("recent or pending row deleted")
	}
}

// TestCreateStoresArgsJSONBRejects: args json.Valid accepts but jsonb
// rejects are stored as a JSON string and still prompt.
func TestCreateStoresArgsJSONBRejects(t *testing.T) {
	pool := integrationPermPool(t)
	ctx := t.Context()
	sid := chatSession(t, pool)
	b := NewPermBroker()
	b.SetStore(NewPGPermStore(pool), nil, nil)
	for _, raw := range []string{`{"command":"printf '\u0000'"}`, `{"n":1e999999}`} {
		p := PendingPermission{SessionID: sid, Tool: "shell", Args: json.RawMessage(raw), OriginKind: PermOriginChat}
		id, ch, err := b.Create(ctx, p)
		if err != nil {
			t.Fatalf("Create(%s): %v", raw, err)
		}
		if len(ch) != 0 {
			t.Fatalf("Create(%s) came back answered, want a pending prompt", raw)
		}
		db, _ := pool.Get()
		var stored string
		if err := db.QueryRow(ctx, `SELECT args #>> '{}' FROM pending_permissions WHERE id = $1`, id).Scan(&stored); err != nil {
			t.Fatalf("read args: %v", err)
		}
		if stored != raw {
			t.Fatalf("stored args = %q, want the raw call %q as a string", stored, raw)
		}
		again, _, err := b.Create(ctx, p)
		if err != nil || again == id {
			t.Fatalf("second ask = %q, %v; want its own prompt (the first is live)", again, err)
		}
	}
}
