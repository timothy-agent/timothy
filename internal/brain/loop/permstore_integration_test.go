//go:build integration

package loop

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
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
