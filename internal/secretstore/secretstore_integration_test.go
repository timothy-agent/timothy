//go:build integration

package secretstore

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	if configured, _, err := s.Status(ctx, ref); err != nil || configured {
		t.Fatalf("Status before Set: configured=%v err=%v", configured, err)
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
	if configured, backend, err := s.Status(ctx, ref); err != nil || !configured || backend != "db" {
		t.Fatalf("Status after Set: configured=%v backend=%q err=%v", configured, backend, err)
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

// TestVaultResolve drives the vault backend end to end against a fake
// KV v2 server: config + token stored through the store's own API,
// then Resolve fetches through HTTP.
func TestVaultResolve(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	ref := "TEST_VAULT_" + t.Name()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "vault-tok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/v1/kv/data/timothy/anthropic":
			w.Write([]byte(`{"data":{"data":{"api_key":"sk-from-vault"}}}`))
		case "/v1/auth/token/lookup-self":
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := `{"address":"` + srv.URL + `","mount":"kv","token_ref":"` + ref + `_TOKEN"}`
	if _, err := s.SetBackendConfig(ctx, "vault", []byte(cfg)); err != nil {
		t.Fatalf("SetBackendConfig: %v", err)
	}
	t.Cleanup(func() {
		db, _ := s.db.Get()
		db.Exec(context.Background(), `DELETE FROM secret_backend_config WHERE backend = 'vault'`)
		db.Exec(context.Background(), `DELETE FROM secrets WHERE ref_name LIKE 'TEST_VAULT_%'`)
	})
	if err := s.Set(ctx, ref+"_TOKEN", "vault-tok"); err != nil {
		t.Fatalf("Set token: %v", err)
	}
	if err := s.SetExternal(ctx, ref, "vault", "timothy/anthropic#api_key"); err != nil {
		t.Fatalf("SetExternal: %v", err)
	}

	got, err := s.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "sk-from-vault" {
		t.Fatalf("Resolve: got %q, want sk-from-vault", got)
	}

	if err := s.TestBackend(ctx, "vault"); err != nil {
		t.Fatalf("TestBackend: %v", err)
	}

	if configured, backend, err := s.Status(ctx, ref); err != nil || !configured || backend != "vault" {
		t.Fatalf("Status: configured=%v backend=%q err=%v", configured, backend, err)
	}
}
