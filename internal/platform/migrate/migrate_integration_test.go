//go:build integration

package migrate

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration tests run against a real Postgres:
//
//	make test-integration   (compose stack must be up)
//
// Fixture migrations use the 99xx_itest prefix and are cleaned up so
// repeated runs and the real schema never collide.

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func cleanup(t *testing.T, pool *pgxpool.Pool, versions []string, tables []string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, v := range versions {
			_, _ = pool.Exec(ctx, "DELETE FROM schema_migrations WHERE version = $1", v)
		}
		for _, tbl := range tables {
			_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl)
		}
	})
}

func TestRunAppliesAndIsIdempotent(t *testing.T) {
	pool := testPool(t)
	files := fstest.MapFS{
		"9901_itest_a.sql": {Data: []byte(
			"CREATE TABLE IF NOT EXISTS itest_a (id int PRIMARY KEY); INSERT INTO itest_a VALUES (1);")},
		"9902_itest_b.sql": {Data: []byte(
			"CREATE TABLE IF NOT EXISTS itest_b (id int PRIMARY KEY);")},
	}
	cleanup(t, pool, []string{"9901_itest_a.sql", "9902_itest_b.sql"}, []string{"itest_a", "itest_b"})

	if err := Run(t.Context(), pool, files, discard()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	// Second run must skip: the INSERT in 9901 would violate the PK if
	// the migration executed again.
	if err := Run(t.Context(), pool, files, discard()); err != nil {
		t.Fatalf("second Run (idempotency): %v", err)
	}

	var n int
	if err := pool.QueryRow(t.Context(),
		"SELECT count(*) FROM schema_migrations WHERE version LIKE '990%_itest%'",
	).Scan(&n); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if n != 2 {
		t.Fatalf("recorded versions = %d, want 2", n)
	}
}

func TestRunRollsBackFailedMigration(t *testing.T) {
	pool := testPool(t)
	files := fstest.MapFS{
		"9911_itest_bad.sql": {Data: []byte(
			"CREATE TABLE itest_bad (id int); SELECT no_such_function();")},
	}
	cleanup(t, pool, []string{"9911_itest_bad.sql"}, []string{"itest_bad"})

	if err := Run(t.Context(), pool, files, discard()); err == nil {
		t.Fatal("Run() = nil, want error from bad SQL")
	}

	// The failed migration's transaction must have rolled back fully:
	// no table, no recorded version.
	var exists bool
	if err := pool.QueryRow(t.Context(),
		"SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name = 'itest_bad')",
	).Scan(&exists); err != nil {
		t.Fatalf("check table: %v", err)
	}
	if exists {
		t.Fatal("itest_bad exists after failed migration (no rollback)")
	}
	if err := pool.QueryRow(t.Context(),
		"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = '9911_itest_bad.sql')",
	).Scan(&exists); err != nil {
		t.Fatalf("check version: %v", err)
	}
	if exists {
		t.Fatal("failed migration recorded in schema_migrations")
	}
}

func TestRunConcurrentStartersRaceSafely(t *testing.T) {
	pool := testPool(t)
	files := fstest.MapFS{
		"9921_itest_race.sql": {Data: []byte(
			"CREATE TABLE itest_race (id int PRIMARY KEY); INSERT INTO itest_race VALUES (1);")},
	}
	cleanup(t, pool, []string{"9921_itest_race.sql"}, []string{"itest_race"})

	// Deliberately NOT "IF NOT EXISTS" and with a PK-violating INSERT:
	// if the advisory lock fails to serialize appliers, one of them
	// errors.
	const workers = 4
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = Run(context.Background(), pool, files, discard())
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}

	var n int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM itest_race").Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 1 {
		t.Fatalf("itest_race rows = %d, want 1 (applied exactly once)", n)
	}
}
