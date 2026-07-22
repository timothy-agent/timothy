//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func integrationPermissions(t *testing.T) (*Permissions, string) {
	t.Helper()
	o := integrationOutputs(t) // reuses pool + migrations
	sid := newSessionID(t, o)
	return NewPermissions(o.db, "/workspace"), sid
}

func shellCall(command string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"command": command})
	return raw
}

// sweepAllowlist removes the fixture patterns this file seeds.
func sweepAllowlist(ctx context.Context, db execer) {
	_, _ = db.Exec(ctx,
		"DELETE FROM project_allowlist WHERE tool = 'shell' AND pattern IN ('git status*', 'rm*')")
}

// cleanupAllowlist sweeps on an independent connection: t.Context is
// canceled before cleanups run and the pool may already be closed.
func cleanupAllowlist(t *testing.T) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		sweepAllowlist(ctx, conn)
	}
}

func TestResolveNoGrantAsks(t *testing.T) {
	p, sid := integrationPermissions(t)

	res, err := p.Resolve(t.Context(), sid, "shell", shellCall("ls -la"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Decision != DecisionAsk {
		t.Fatalf("res = %+v, want ask", res)
	}
}

func TestResolveSessionGrantAllows(t *testing.T) {
	p, sid := integrationPermissions(t)

	if err := p.Grant(t.Context(), sid, "shell", "ls*", time.Hour); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	res, err := p.Resolve(t.Context(), sid, "shell", shellCall("ls -la src/"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Decision != DecisionAllow {
		t.Fatalf("res = %+v, want allow", res)
	}

	// The grant is scoped to its session.
	o := integrationOutputs(t)
	other := newSessionID(t, o)
	res, err = p.Resolve(t.Context(), other, "shell", shellCall("ls -la src/"))
	if err != nil {
		t.Fatalf("Resolve other session: %v", err)
	}
	if res.Decision != DecisionAsk {
		t.Fatalf("other-session res = %+v, want ask", res)
	}
}

func TestResolveExpiredGrantAsks(t *testing.T) {
	p, sid := integrationPermissions(t)

	if err := p.Grant(t.Context(), sid, "shell", "wc*", time.Hour); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	db, err := p.db.Get()
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if _, err := db.Exec(t.Context(),
		"UPDATE session_grants SET expires = now() - interval '1 minute' WHERE session_id = $1", sid,
	); err != nil {
		t.Fatalf("expire: %v", err)
	}

	res, err := p.Resolve(t.Context(), sid, "shell", shellCall("wc -l notes.md"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Decision != DecisionAsk {
		t.Fatalf("res = %+v, want ask after expiry", res)
	}
}

func TestResolveProjectAllowlistAllows(t *testing.T) {
	p, sid := integrationPermissions(t)

	db, err := p.db.Get()
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	// Sweep at setup too — a run that dies before t.Cleanup registers
	// leaks its fixtures.
	sweepAllowlist(t.Context(), db)
	if _, err := db.Exec(t.Context(),
		"INSERT INTO project_allowlist (tool, pattern) VALUES ('shell', 'git status*') ON CONFLICT DO NOTHING",
	); err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}
	t.Cleanup(cleanupAllowlist(t))

	res, err := p.Resolve(t.Context(), sid, "shell", shellCall("git status --short"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Decision != DecisionAllow {
		t.Fatalf("res = %+v, want allow", res)
	}

	// Destructive commands ignore the allowlist entirely.
	if _, err := db.Exec(t.Context(),
		"INSERT INTO project_allowlist (tool, pattern) VALUES ('shell', 'rm*') ON CONFLICT DO NOTHING",
	); err != nil {
		t.Fatalf("seed rm allowlist: %v", err)
	}
	res, err = p.Resolve(t.Context(), sid, "shell", shellCall("rm -rf build/"))
	if err != nil {
		t.Fatalf("Resolve rm: %v", err)
	}
	if res.Decision != DecisionAsk || res.Danger != DangerDestructive {
		t.Fatalf("res = %+v, want ask despite allowlist", res)
	}
}
