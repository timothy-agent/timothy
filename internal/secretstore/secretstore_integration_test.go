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
		// A transient pool failure here must not panic the run — the
		// next run's setup sweep gets another chance at the leftovers.
		db, err := s.db.Get()
		if err != nil {
			t.Logf("cleanup skipped: %v", err)
			return
		}
		_, _ = db.Exec(context.Background(), `DELETE FROM secret_backend_config WHERE backend = 'vault'`)
		_, _ = db.Exec(context.Background(), `DELETE FROM secrets WHERE ref_name LIKE 'TEST_VAULT_%'`)
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

// Default backend: single flag, external backends must be configured
// before they can take it, and removing the default's config hands the
// flag back to built-in storage.
func TestDefaultBackendLifecycle(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()

	origCfg, err := s.GetBackendConfig(ctx, "vault")
	if err != nil {
		t.Fatalf("GetBackendConfig: %v", err)
	}
	origDefault, err := s.DefaultBackend(ctx)
	if err != nil {
		t.Fatalf("DefaultBackend: %v", err)
	}
	// The backend config table is shared state (a dev database may hold
	// a real vault config): restore what was there. A defer, not
	// t.Cleanup — it runs while t.Context() is still alive.
	defer func() {
		if string(origCfg) != "{}" {
			if _, err := s.SetBackendConfig(ctx, "vault", origCfg); err != nil {
				t.Errorf("restore vault config: %v", err)
			}
		} else {
			_ = s.DeleteBackendConfig(ctx, "vault")
		}
		if err := s.SetDefaultBackend(ctx, origDefault); err != nil {
			t.Errorf("restore default backend: %v", err)
		}
	}()

	if err := s.SetDefaultBackend(ctx, "nope"); err == nil {
		t.Fatal("unknown backend accepted as default")
	}
	if err := s.DeleteBackendConfig(ctx, "vault"); err != nil {
		t.Fatalf("DeleteBackendConfig: %v", err)
	}
	if err := s.SetDefaultBackend(ctx, "vault"); err == nil {
		t.Fatal("unconfigured vault accepted as default")
	}

	if _, err := s.SetBackendConfig(ctx, "vault", []byte(`{"address":"http://127.0.0.1:1"}`)); err != nil {
		t.Fatalf("SetBackendConfig: %v", err)
	}
	if err := s.SetDefaultBackend(ctx, "vault"); err != nil {
		t.Fatalf("SetDefaultBackend vault: %v", err)
	}
	if got, err := s.DefaultBackend(ctx); err != nil || got != "vault" {
		t.Fatalf("DefaultBackend = %q, %v; want vault", got, err)
	}
	backends, err := s.Backends(ctx)
	if err != nil {
		t.Fatalf("Backends: %v", err)
	}
	for _, b := range backends {
		if b.Default != (b.Backend == "vault") {
			t.Fatalf("Backends = %+v, want only vault default", backends)
		}
	}

	// Deleting the default's config falls back to built-in storage.
	if err := s.DeleteBackendConfig(ctx, "vault"); err != nil {
		t.Fatalf("DeleteBackendConfig: %v", err)
	}
	if got, err := s.DefaultBackend(ctx); err != nil || got != "db" {
		t.Fatalf("DefaultBackend after delete = %q, %v; want db", got, err)
	}
}
