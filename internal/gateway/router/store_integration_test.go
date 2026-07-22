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

	// Providers are never seeded — only the route names the code
	// references (0002), empty-chained and disabled. Disabled routes
	// don't enter the snapshot, so assert against the table itself.
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, want := range []string{"default", "summarize", "embedding", "research"} {
		var n int
		if err := db.QueryRow(t.Context(),
			"SELECT count(*) FROM routes WHERE name = $1", want).Scan(&n); err != nil {
			t.Fatalf("count route %s: %v", want, err)
		}
		if n != 1 {
			t.Fatalf("seeded route %q missing", want)
		}
	}

	// State-independent (the dev database's enabled flags are live
	// config, not fixture data): an unrouted category must resolve to
	// a structured error, never panic.
	if _, err := snap.Resolve("no-such-route", "", Sticky{}); err == nil {
		t.Fatal("Resolve succeeded for a category with no route")
	}
}

func TestStoreReloadReflectsSQLChanges(t *testing.T) {
	s := integrationStore(t)
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Fixture rows OWNED by this test: the dev database's real
	// providers and routes are live configuration and must never be
	// toggled by tests. Sweep at setup too — a run that dies between
	// insert and t.Cleanup registration leaks its fixtures.
	_, _ = db.Exec(t.Context(), "DELETE FROM routes WHERE name = 'itest-cat'")
	_, _ = db.Exec(t.Context(), "DELETE FROM providers WHERE name = 'itest-prov'")
	var providerID string
	if err := db.QueryRow(t.Context(), `INSERT INTO providers
		(name, kind, driver, base_url, default_model, credential_ref, enabled)
		VALUES ('itest-prov', 'api', 'openaicompat', 'https://itest.invalid/v1', 'itest-model', 'ITEST_KEY', true)
		RETURNING id`).Scan(&providerID); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO routes (name, chain, enabled)
		VALUES ('itest-cat', jsonb_build_array(jsonb_build_object('provider_id', $1::uuid, 'model', 'itest-model')), true)`,
		providerID); err != nil {
		t.Fatalf("insert route: %v", err)
	}
	t.Cleanup(func() {
		// t.Context is canceled before cleanups run and the pool's
		// watcher may already have closed it — use an independent
		// connection so the cleanup cannot be lost.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, "DELETE FROM routes WHERE name = 'itest-cat'"); err != nil {
			t.Errorf("cleanup route: %v", err)
		}
		if _, err := conn.Exec(ctx, "DELETE FROM providers WHERE name = 'itest-prov'"); err != nil {
			t.Errorf("cleanup provider: %v", err)
		}
	})

	t.Setenv("ITEST_KEY", "test-key-value") // healthy via env lookup
	if err := s.Load(t.Context()); err != nil {
		t.Fatalf("reload: %v", err)
	}

	attempts, err := s.Snapshot().Resolve("itest-cat", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve after insert: %v", err)
	}
	if len(attempts) != 1 || attempts[0].ProviderName != "itest-prov" || attempts[0].Model != "itest-model" {
		t.Fatalf("attempts = %+v, want itest-prov/itest-model", attempts)
	}
}
