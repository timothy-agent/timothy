//go:build integration

package settings

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

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
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), "DELETE FROM settings")
		_, _ = db.Exec(context.Background(), "DELETE FROM admin_audit WHERE entity = 'setting'")
	})
	return New(pool, log)
}

func TestSettingsDefaultsFlipAndAudit(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	// Absent rows read as enabled.
	if !s.Enabled(ctx, KeyTools) || !s.Enabled(ctx, KeyCompaction) {
		t.Fatal("defaults must be enabled")
	}

	if err := s.Set(ctx, KeyTools, false); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if s.Enabled(ctx, KeyTools) {
		t.Fatal("flip did not take effect (cache must invalidate on write)")
	}
	if !s.Enabled(ctx, KeyCompaction) {
		t.Fatal("unrelated switch flipped")
	}

	if err := s.Set(ctx, "not_a_setting", true); err == nil {
		t.Fatal("unknown key must refuse")
	}

	// The flip left an audit row.
	pool := s.db
	db, _ := pool.Get()
	var n int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM admin_audit
		WHERE entity = 'setting' AND entity_id = $1`, KeyTools).Scan(&n); err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if n == 0 {
		t.Fatal("no audit row for the settings flip")
	}
}
