//go:build integration

package agents

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const marker = "itest-agent-"

func testStore(t *testing.T) *Store {
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
	// Sweep at setup AND teardown; the teardown uses a fresh one-shot
	// connection because the pool dies with t.Context().
	_, _ = db.Exec(ctx, "DELETE FROM agents WHERE name LIKE $1 || '%'", marker)
	_, _ = db.Exec(ctx, "DELETE FROM admin_audit WHERE entity = 'agent'")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Logf("teardown sweep skipped: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		_, _ = conn.Exec(ctx, "DELETE FROM agents WHERE name LIKE $1 || '%'", marker)
		_, _ = conn.Exec(ctx, "DELETE FROM admin_audit WHERE entity = 'agent'")
	})
	return NewStore(pool, log)
}

func TestAgentCRUDAndResolve(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	name := marker + "researcher"
	id, err := s.Create(ctx, Agent{
		Name: name, Description: "d", PromptOverlay: "overlay",
		Route: "research", Skills: []string{"research-task"},
		Tools: []string{"web_search"}, Memory: true, Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Create(ctx, Agent{Name: "Bad Name!"}); err == nil {
		t.Fatal("invalid name accepted")
	}

	a, ok := s.Resolve(ctx, name)
	if !ok || a.Route != "research" || a.PromptOverlay != "overlay" || len(a.Tools) != 1 {
		t.Fatalf("Resolve = %+v ok=%v", a, ok)
	}
	// Unknown names fall back to the default but report false.
	if _, ok := s.Resolve(ctx, marker+"ghost"); ok {
		t.Fatal("unknown agent resolved as known")
	}
	// Empty name resolves the seeded default.
	def, ok := s.Resolve(ctx, "")
	if !ok || def.Name != "general" {
		t.Fatalf("default agent = %+v ok=%v, want seeded general", def, ok)
	}

	route := "default"
	if err := s.Patch(ctx, id, Patch{Route: &route}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if a, _ := s.Resolve(ctx, name); a.Route != "default" {
		t.Fatalf("patched route = %q (cache must invalidate)", a.Route)
	}

	// The seeded default is protected; moving the flag frees it.
	agentsList, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var generalID string
	for _, a := range agentsList {
		if a.Name == "general" {
			generalID = a.ID
		}
	}
	if err := s.Delete(ctx, generalID); err == nil {
		t.Fatal("default agent deletion allowed")
	}
	if err := s.SetDefault(ctx, id); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}
	defer func() {
		// Hand the flag back so other tests (and the dev DB) keep the
		// seeded default.
		if err := s.SetDefault(ctx, generalID); err != nil {
			t.Errorf("restore default: %v", err)
		}
	}()
	if def, _ := s.Resolve(ctx, ""); def.Name != name {
		t.Fatalf("default after SetDefault = %q, want %s", def.Name, name)
	}
	if err := s.Delete(ctx, id); err == nil {
		t.Fatal("new default agent deletion allowed")
	}
}
