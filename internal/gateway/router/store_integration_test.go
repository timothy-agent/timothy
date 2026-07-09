//go:build integration

package router

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

func integrationStore(t *testing.T) *Store {
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
	return NewStore(pool, os.Getenv, log)
}

func TestStoreLoadsSeededConfig(t *testing.T) {
	s := integrationStore(t)

	if err := s.Load(t.Context()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	snap := s.Snapshot()
	if snap == nil {
		t.Fatal("nil snapshot after successful load")
	}

	rows, _ := snap.Providers()
	names := map[string]bool{}
	for _, r := range rows {
		names[r.Name] = true
	}
	for _, want := range []string{"anthropic", "zai-glm", "xai-grok"} {
		if !names[want] {
			t.Fatalf("seeded provider %q missing; have %v", want, names)
		}
	}

	// State-independent (the dev database's enabled flags are live
	// config, not fixture data): an unrouted category must resolve to
	// a structured error, never panic.
	if _, err := snap.Resolve("no-such-category", ""); err == nil {
		t.Fatal("Resolve succeeded for a category with no route")
	}
}

func TestStoreReloadReflectsSQLChanges(t *testing.T) {
	s := integrationStore(t)
	db, _ := s.db.Get()

	if _, err := db.Exec(t.Context(),
		"UPDATE providers SET enabled = true WHERE name = 'zai-glm'"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := db.Exec(t.Context(),
		"UPDATE task_routes SET enabled = true WHERE task_category = 'coding'"); err != nil {
		t.Fatalf("enable route: %v", err)
	}
	t.Cleanup(func() {
		// t.Context is canceled before cleanups run and the pool's
		// watcher may already have closed it — use an independent
		// connection so the reset cannot be lost.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, "UPDATE providers SET enabled = false WHERE name = 'zai-glm'"); err != nil {
			t.Errorf("cleanup provider: %v", err)
		}
		if _, err := conn.Exec(ctx, "UPDATE task_routes SET enabled = false WHERE task_category = 'coding'"); err != nil {
			t.Errorf("cleanup route: %v", err)
		}
	})

	t.Setenv("ZAI_API_KEY", "test-key-value") // healthy via env lookup
	if err := s.Load(t.Context()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	attempts, err := s.Snapshot().Resolve("coding", "")
	if err != nil {
		t.Fatalf("Resolve after enable: %v", err)
	}
	found := false
	for _, a := range attempts {
		if a.ProviderName == "zai-glm" && a.Model == "glm-4.7" {
			found = true
		}
	}
	if !found {
		t.Fatalf("zai-glm/glm-4.7 not in attempts after enable: %+v", attempts)
	}
}
