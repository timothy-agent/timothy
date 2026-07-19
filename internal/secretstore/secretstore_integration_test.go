//go:build integration

package secretstore

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

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
	key := make([]byte, keyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	s, err := New(pool, key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestStoreSetResolveDelete(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	ref := "TEST_SECRET_" + t.Name()

	if _, err := s.Resolve(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve before Set: got %v, want ErrNotFound", err)
	}
	if has, _ := s.Has(ctx, ref); has {
		t.Fatalf("Has before Set: got true")
	}

	if err := s.Set(ctx, ref, "sk-abc123"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("Resolve after Set: %v", err)
	}
	if got != "sk-abc123" {
		t.Fatalf("Resolve: got %q, want sk-abc123", got)
	}
	if has, _ := s.Has(ctx, ref); !has {
		t.Fatalf("Has after Set: got false")
	}

	// Overwrite: last Set wins.
	if err := s.Set(ctx, ref, "sk-rotated"); err != nil {
		t.Fatalf("Set (rotate): %v", err)
	}
	got, err = s.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("Resolve after rotate: %v", err)
	}
	if got != "sk-rotated" {
		t.Fatalf("Resolve after rotate: got %q, want sk-rotated", got)
	}

	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Resolve(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve after Delete: got %v, want ErrNotFound", err)
	}
}
