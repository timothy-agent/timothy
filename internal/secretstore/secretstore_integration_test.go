//go:build integration

package secretstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
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

// TestVaultWriteThrough drives the vault backend end to end against a
// fake KV v2 server: with vault as the store-wide default, Set must
// write the raw value into vault itself (not just record a reference),
// Resolve must read it back through HTTP, and TestBackend's write
// probe and Delete's external cleanup must round-trip against the
// same fake mount.
func TestVaultWriteThrough(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	ref := "TEST_VAULT_" + t.Name()

	kv := map[string]string{} // path -> value, simulates the KV v2 mount
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "vault-tok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch {
		case r.URL.Path == "/v1/auth/token/lookup-self":
			w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/v1/kv/data/"):
			path := strings.TrimPrefix(r.URL.Path, "/v1/kv/data/")
			switch r.Method {
			case http.MethodPost:
				var body struct {
					Data struct {
						Value string `json:"value"`
					} `json:"data"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				kv[path] = body.Data.Value
				w.Write([]byte(`{}`))
			case http.MethodGet:
				val, ok := kv[path]
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = w.Write([]byte(`{"data":{"data":{"value":` + strconv.Quote(val) + `}}}`))
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		case strings.HasPrefix(r.URL.Path, "/v1/kv/metadata/") && r.Method == http.MethodDelete:
			delete(kv, strings.TrimPrefix(r.URL.Path, "/v1/kv/metadata/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// Sweep leftovers from a crashed run, then save the shared vault
	// config/default so they can be restored — a dev database may hold
	// real ones.
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_, _ = db.Exec(ctx, `DELETE FROM secrets WHERE ref_name LIKE 'TEST_VAULT_%'`)
	origCfg, err := s.GetBackendConfig(ctx, "vault")
	if err != nil {
		t.Fatalf("GetBackendConfig: %v", err)
	}
	origDefault, err := s.DefaultBackend(ctx)
	if err != nil {
		t.Fatalf("DefaultBackend: %v", err)
	}
	// A defer, not t.Cleanup — it runs while t.Context() and the pool
	// are still alive, so the sweep cannot be silently lost.
	defer func() {
		if string(origCfg) != "{}" {
			if _, err := s.SetBackendConfig(ctx, "vault", origCfg); err != nil {
				t.Errorf("restore vault config: %v", err)
			}
		} else if err := s.DeleteBackendConfig(ctx, "vault"); err != nil {
			t.Errorf("delete vault config: %v", err)
		}
		if err := s.SetDefaultBackend(ctx, origDefault); err != nil {
			t.Errorf("restore default backend: %v", err)
		}
		if _, err := db.Exec(ctx, `DELETE FROM secrets WHERE ref_name LIKE 'TEST_VAULT_%'`); err != nil {
			t.Errorf("sweep vault secrets: %v", err)
		}
	}()

	cfg := `{"address":"` + srv.URL + `","mount":"kv","token_ref":"` + ref + `_TOKEN"}`
	if _, err := s.SetBackendConfig(ctx, "vault", []byte(cfg)); err != nil {
		t.Fatalf("SetBackendConfig: %v", err)
	}
	// SetDB, not Set: the vault token is bootstrap for vault itself and
	// must land in built-in storage regardless of the default backend
	// (which is not vault yet at this point anyway).
	if err := s.SetDB(ctx, ref+"_TOKEN", "vault-tok"); err != nil {
		t.Fatalf("SetDB token: %v", err)
	}
	if err := s.SetDefaultBackend(ctx, "vault"); err != nil {
		t.Fatalf("SetDefaultBackend: %v", err)
	}

	if err := s.Set(ctx, ref, "sk-from-vault"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if kv["timothy/"+ref] != "sk-from-vault" {
		t.Fatalf("vault mount holds %q, want sk-from-vault", kv["timothy/"+ref])
	}

	got, err := s.Resolve(ctx, ref)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "sk-from-vault" {
		t.Fatalf("Resolve: got %q, want sk-from-vault", got)
	}
	if configured, backend, err := s.Status(ctx, ref); err != nil || !configured || backend != "vault" {
		t.Fatalf("Status: configured=%v backend=%q err=%v", configured, backend, err)
	}

	if err := s.TestBackend(ctx, "vault"); err != nil {
		t.Fatalf("TestBackend: %v", err)
	}
	if _, ok := kv["timothy/__probe"]; ok {
		t.Error("TestBackend left its write probe behind in vault")
	}

	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := kv["timothy/"+ref]; ok {
		t.Error("Delete left the secret behind in vault")
	}
	if _, err := s.Resolve(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve after Delete: got %v, want ErrNotFound", err)
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

// fakeVaultKV serves a minimal KV v2 mount for TestMigrate*: same
// shape as TestVaultWriteThrough's inline fake, factored out so both
// migrate directions can share one server.
func fakeVaultKV(t *testing.T) (*httptest.Server, map[string]string) {
	t.Helper()
	kv := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "vault-tok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch {
		case r.URL.Path == "/v1/auth/token/lookup-self":
			w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/v1/kv/data/"):
			path := strings.TrimPrefix(r.URL.Path, "/v1/kv/data/")
			switch r.Method {
			case http.MethodPost:
				var body struct {
					Data struct {
						Value string `json:"value"`
					} `json:"data"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				kv[path] = body.Data.Value
				w.Write([]byte(`{}`))
			case http.MethodGet:
				val, ok := kv[path]
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = w.Write([]byte(`{"data":{"data":{"value":` + strconv.Quote(val) + `}}}`))
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		case strings.HasPrefix(r.URL.Path, "/v1/kv/metadata/") && r.Method == http.MethodDelete:
			delete(kv, strings.TrimPrefix(r.URL.Path, "/v1/kv/metadata/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return srv, kv
}

// setUpVaultDefault points s at a fake vault mount and makes it the
// store-wide default, returning a restore func for the caller to defer
// (mirrors TestVaultWriteThrough's own save/restore dance since the
// backend config table is shared state).
func setUpVaultDefault(t *testing.T, s *Store, srv *httptest.Server, tokenRef string) func() {
	t.Helper()
	ctx := t.Context()
	origCfg, err := s.GetBackendConfig(ctx, "vault")
	if err != nil {
		t.Fatalf("GetBackendConfig: %v", err)
	}
	origDefault, err := s.DefaultBackend(ctx)
	if err != nil {
		t.Fatalf("DefaultBackend: %v", err)
	}
	cfg := `{"address":"` + srv.URL + `","mount":"kv","token_ref":"` + tokenRef + `"}`
	if _, err := s.SetBackendConfig(ctx, "vault", []byte(cfg)); err != nil {
		t.Fatalf("SetBackendConfig: %v", err)
	}
	if err := s.SetDB(ctx, tokenRef, "vault-tok"); err != nil {
		t.Fatalf("SetDB token: %v", err)
	}
	if err := s.SetDefaultBackend(ctx, "vault"); err != nil {
		t.Fatalf("SetDefaultBackend: %v", err)
	}
	return func() {
		if string(origCfg) != "{}" {
			if _, err := s.SetBackendConfig(ctx, "vault", origCfg); err != nil {
				t.Errorf("restore vault config: %v", err)
			}
		} else if err := s.DeleteBackendConfig(ctx, "vault"); err != nil {
			t.Errorf("delete vault config: %v", err)
		}
		if err := s.SetDefaultBackend(ctx, origDefault); err != nil {
			t.Errorf("restore default backend: %v", err)
		}
	}
}

// TestMigrateDBToVault drives Migrate's db->external direction: the
// value written under backend='db' must resolve identically after
// Migrate, live in the fake vault mount, and the db ciphertext/nonce
// must be gone (cleared by upsertRef in step 2).
func TestMigrateDBToVault(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	ref := "TEST_MIGRATE_DBVAULT_" + t.Name()
	tokenRef := ref + "_TOKEN"

	srv, kv := fakeVaultKV(t)
	defer srv.Close()
	restore := setUpVaultDefault(t, s, srv, tokenRef)
	defer restore()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() {
		_, _ = db.Exec(ctx, `DELETE FROM secrets WHERE ref_name IN ($1, $2)`, ref, tokenRef)
	}()

	// Set through db explicitly (SetDB, not Set — the default is vault
	// by now), so the starting point is a real db-backed row.
	if err := s.SetDB(ctx, ref, "sk-orig"); err != nil {
		t.Fatalf("SetDB: %v", err)
	}

	if err := s.Migrate(ctx, ref, "vault"); err != nil {
		t.Fatalf("Migrate db->vault: %v", err)
	}
	if kv["timothy/"+ref] != "sk-orig" {
		t.Fatalf("vault mount holds %q, want sk-orig", kv["timothy/"+ref])
	}
	if configured, backend, err := s.Status(ctx, ref); err != nil || !configured || backend != "vault" {
		t.Fatalf("Status after migrate: configured=%v backend=%q err=%v", configured, backend, err)
	}
	got, err := s.Resolve(ctx, ref)
	if err != nil || got != "sk-orig" {
		t.Fatalf("Resolve after migrate = (%q, %v), want (sk-orig, nil)", got, err)
	}
	var ciphertext []byte
	if err := db.QueryRow(ctx, `SELECT ciphertext FROM secrets WHERE ref_name = $1`, ref).Scan(&ciphertext); err != nil {
		t.Fatalf("query ciphertext: %v", err)
	}
	if ciphertext != nil {
		t.Error("db ciphertext survived a migrate to vault")
	}

	// Idempotent: migrating again to the same backend is a no-op, not
	// a second write.
	if err := s.Migrate(ctx, ref, "vault"); err != nil {
		t.Fatalf("Migrate (idempotent): %v", err)
	}
}

// TestMigrateVaultToDB drives Migrate's external->db direction and
// confirms the old vault copy is deleted (step 3's cleanup).
func TestMigrateVaultToDB(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	ref := "TEST_MIGRATE_VAULTDB_" + t.Name()
	tokenRef := ref + "_TOKEN"

	srv, kv := fakeVaultKV(t)
	defer srv.Close()
	restore := setUpVaultDefault(t, s, srv, tokenRef)
	defer restore()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() {
		_, _ = db.Exec(ctx, `DELETE FROM secrets WHERE ref_name IN ($1, $2)`, ref, tokenRef)
	}()

	// Set through the (now vault) default so the starting point is a
	// real vault-backed row.
	if err := s.Set(ctx, ref, "sk-vault"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := s.Migrate(ctx, ref, "db"); err != nil {
		t.Fatalf("Migrate vault->db: %v", err)
	}
	if configured, backend, err := s.Status(ctx, ref); err != nil || !configured || backend != "db" {
		t.Fatalf("Status after migrate: configured=%v backend=%q err=%v", configured, backend, err)
	}
	got, err := s.Resolve(ctx, ref)
	if err != nil || got != "sk-vault" {
		t.Fatalf("Resolve after migrate = (%q, %v), want (sk-vault, nil)", got, err)
	}
	if _, ok := kv["timothy/"+ref]; ok {
		t.Error("Migrate vault->db left the old vault copy behind")
	}
}

// TestMigrateUnknownTargetErrors pins that Migrate rejects a target
// backend name outside {db, vault, asm} before touching the row.
func TestMigrateUnknownTargetErrors(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	ref := "TEST_MIGRATE_UNKNOWN_" + t.Name()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() {
		_, _ = db.Exec(ctx, `DELETE FROM secrets WHERE ref_name = $1`, ref)
	}()

	if err := s.SetDB(ctx, ref, "sk-x"); err != nil {
		t.Fatalf("SetDB: %v", err)
	}
	if err := s.Migrate(ctx, ref, "nope"); err == nil {
		t.Fatal("unknown target backend accepted")
	}
	if configured, backend, err := s.Status(ctx, ref); err != nil || !configured || backend != "db" {
		t.Fatalf("row changed after a rejected migrate: configured=%v backend=%q err=%v", configured, backend, err)
	}
}

// TestMigrateUnconfiguredTargetErrors pins that Migrate refuses a known
// but unconfigured external backend (no vault/asm connection config
// saved) — the write in step 1 fails cleanly (vaultConfig/asmClient
// error), leaving the row on its original backend.
func TestMigrateUnconfiguredTargetErrors(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	ref := "TEST_MIGRATE_NOCFG_" + t.Name()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	origCfg, err := s.GetBackendConfig(ctx, "vault")
	if err != nil {
		t.Fatalf("GetBackendConfig: %v", err)
	}
	defer func() {
		_, _ = db.Exec(ctx, `DELETE FROM secrets WHERE ref_name = $1`, ref)
		if string(origCfg) != "{}" {
			if _, err := s.SetBackendConfig(ctx, "vault", origCfg); err != nil {
				t.Errorf("restore vault config: %v", err)
			}
		} else if err := s.DeleteBackendConfig(ctx, "vault"); err != nil {
			t.Errorf("delete vault config: %v", err)
		}
	}()
	if err := s.DeleteBackendConfig(ctx, "vault"); err != nil {
		t.Fatalf("DeleteBackendConfig: %v", err)
	}

	if err := s.SetDB(ctx, ref, "sk-x"); err != nil {
		t.Fatalf("SetDB: %v", err)
	}
	if err := s.Migrate(ctx, ref, "vault"); err == nil {
		t.Fatal("unconfigured vault target accepted")
	}
	if configured, backend, err := s.Status(ctx, ref); err != nil || !configured || backend != "db" {
		t.Fatalf("row changed after a rejected migrate: configured=%v backend=%q err=%v", configured, backend, err)
	}
}

// TestMigrateRefusesVaultTokenBootstrapRef pins the chicken-and-egg
// fix: vault's own token_ref must never move into vault itself, since
// resolving it afterward would need the token it no longer has in db.
// Migrating it back to db (the recovery path) stays allowed.
func TestMigrateRefusesVaultTokenBootstrapRef(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	tokenRef := "TEST_MIGRATE_BOOTSTRAP_TOKEN_" + t.Name()

	srv, _ := fakeVaultKV(t)
	defer srv.Close()
	restore := setUpVaultDefault(t, s, srv, tokenRef)
	defer restore()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() {
		_, _ = db.Exec(ctx, `DELETE FROM secrets WHERE ref_name = $1`, tokenRef)
	}()

	err = s.Migrate(ctx, tokenRef, "vault")
	if err == nil {
		t.Fatal("Migrate accepted moving the vault token_ref into vault")
	}
	if !strings.Contains(err.Error(), "bootstrap credential") {
		t.Fatalf("error = %v, want it to mention bootstrap credential", err)
	}
	if configured, backend, err := s.Status(ctx, tokenRef); err != nil || !configured || backend != "db" {
		t.Fatalf("row changed after a rejected migrate: configured=%v backend=%q err=%v", configured, backend, err)
	}

	// Recovery path: migrating the bootstrap ref back to db (its
	// current backend) stays allowed as a no-op.
	if err := s.Migrate(ctx, tokenRef, "db"); err != nil {
		t.Fatalf("Migrate bootstrap ref to db (no-op): %v", err)
	}
}

// TestMigrateRefusesVaultSecretIDBootstrapRef covers the exact
// production incident: approle auth's secret_id_ref (defaulting to
// VAULT_SECRET_ID) must also be refused into vault.
func TestMigrateRefusesVaultSecretIDBootstrapRef(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	secretIDRef := "TEST_MIGRATE_BOOTSTRAP_SECRETID_" + t.Name()

	srv, _ := fakeVaultKV(t)
	defer srv.Close()

	origCfg, err := s.GetBackendConfig(ctx, "vault")
	if err != nil {
		t.Fatalf("GetBackendConfig: %v", err)
	}
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() {
		_, _ = db.Exec(ctx, `DELETE FROM secrets WHERE ref_name = $1`, secretIDRef)
		if string(origCfg) != "{}" {
			if _, err := s.SetBackendConfig(ctx, "vault", origCfg); err != nil {
				t.Errorf("restore vault config: %v", err)
			}
		} else if err := s.DeleteBackendConfig(ctx, "vault"); err != nil {
			t.Errorf("delete vault config: %v", err)
		}
	}()

	cfg := `{"address":"` + srv.URL + `","mount":"kv","auth":"approle","role_id":"role-x","secret_id_ref":"` + secretIDRef + `"}`
	if _, err := s.SetBackendConfig(ctx, "vault", []byte(cfg)); err != nil {
		t.Fatalf("SetBackendConfig: %v", err)
	}
	if err := s.SetDB(ctx, secretIDRef, "secret-id-x"); err != nil {
		t.Fatalf("SetDB: %v", err)
	}

	err = s.Migrate(ctx, secretIDRef, "vault")
	if err == nil {
		t.Fatal("Migrate accepted moving the vault secret_id_ref into vault")
	}
	if !strings.Contains(err.Error(), "bootstrap credential") {
		t.Fatalf("error = %v, want it to mention bootstrap credential", err)
	}
}

// TestMigrateRefusesASMSecretKeyBootstrapRef covers asm's static-keys
// auth: the secret_key_ref (defaulting to AWS_SECRET_ACCESS_KEY) must
// be refused into asm. Only the asm backend config row is saved here
// (no live AWS client) — bootstrapRefs reads stored config only, so it
// picks the ref up without needing working AWS credentials.
func TestMigrateRefusesASMSecretKeyBootstrapRef(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	secretKeyRef := "TEST_MIGRATE_BOOTSTRAP_ASMKEY_" + t.Name()

	origCfg, err := s.GetBackendConfig(ctx, "asm")
	if err != nil {
		t.Fatalf("GetBackendConfig: %v", err)
	}
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() {
		_, _ = db.Exec(ctx, `DELETE FROM secrets WHERE ref_name = $1`, secretKeyRef)
		if string(origCfg) != "{}" {
			if _, err := s.SetBackendConfig(ctx, "asm", origCfg); err != nil {
				t.Errorf("restore asm config: %v", err)
			}
		} else if err := s.DeleteBackendConfig(ctx, "asm"); err != nil {
			t.Errorf("delete asm config: %v", err)
		}
	}()

	cfg := `{"auth":"keys","access_key_id":"AKIAEXAMPLE","secret_key_ref":"` + secretKeyRef + `"}`
	if _, err := s.SetBackendConfig(ctx, "asm", []byte(cfg)); err != nil {
		t.Fatalf("SetBackendConfig: %v", err)
	}
	if err := s.SetDB(ctx, secretKeyRef, "secret-key-x"); err != nil {
		t.Fatalf("SetDB: %v", err)
	}

	err = s.Migrate(ctx, secretKeyRef, "asm")
	if err == nil {
		t.Fatal("Migrate accepted moving the asm secret_key_ref into asm")
	}
	if !strings.Contains(err.Error(), "bootstrap credential") {
		t.Fatalf("error = %v, want it to mention bootstrap credential", err)
	}
}

// TestDeleteRefusesVaultTokenBootstrapRef pins the delete-side lockout
// fix: vault's token_ref must never be deleted while vault is
// configured, since resolving anything else stored in vault would need
// a token that no longer exists anywhere.
func TestDeleteRefusesVaultTokenBootstrapRef(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	tokenRef := "TEST_DELETE_BOOTSTRAP_TOKEN_" + t.Name()

	srv, _ := fakeVaultKV(t)
	defer srv.Close()
	restore := setUpVaultDefault(t, s, srv, tokenRef)
	defer restore()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() {
		_, _ = db.Exec(ctx, `DELETE FROM secrets WHERE ref_name = $1`, tokenRef)
	}()

	err = s.Delete(ctx, tokenRef)
	if err == nil {
		t.Fatal("Delete accepted removing the vault token_ref while vault is configured")
	}
	if !strings.Contains(err.Error(), "bootstrap credential") {
		t.Fatalf("error = %v, want it to mention bootstrap credential", err)
	}
	if configured, backend, err := s.Status(ctx, tokenRef); err != nil || !configured || backend != "db" {
		t.Fatalf("row changed after a rejected delete: configured=%v backend=%q err=%v", configured, backend, err)
	}
}

// TestDeleteRefusesVaultSecretIDBootstrapRef covers approle auth's
// secret_id_ref (defaulting to VAULT_SECRET_ID) — the same production
// incident Migrate's guard covers, on the delete path.
func TestDeleteRefusesVaultSecretIDBootstrapRef(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	secretIDRef := "TEST_DELETE_BOOTSTRAP_SECRETID_" + t.Name()

	srv, _ := fakeVaultKV(t)
	defer srv.Close()

	origCfg, err := s.GetBackendConfig(ctx, "vault")
	if err != nil {
		t.Fatalf("GetBackendConfig: %v", err)
	}
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() {
		_, _ = db.Exec(ctx, `DELETE FROM secrets WHERE ref_name = $1`, secretIDRef)
		if string(origCfg) != "{}" {
			if _, err := s.SetBackendConfig(ctx, "vault", origCfg); err != nil {
				t.Errorf("restore vault config: %v", err)
			}
		} else if err := s.DeleteBackendConfig(ctx, "vault"); err != nil {
			t.Errorf("delete vault config: %v", err)
		}
	}()

	cfg := `{"address":"` + srv.URL + `","mount":"kv","auth":"approle","role_id":"role-x","secret_id_ref":"` + secretIDRef + `"}`
	if _, err := s.SetBackendConfig(ctx, "vault", []byte(cfg)); err != nil {
		t.Fatalf("SetBackendConfig: %v", err)
	}
	if err := s.SetDB(ctx, secretIDRef, "secret-id-x"); err != nil {
		t.Fatalf("SetDB: %v", err)
	}

	err = s.Delete(ctx, secretIDRef)
	if err == nil {
		t.Fatal("Delete accepted removing the vault secret_id_ref while vault approle is configured")
	}
	if !strings.Contains(err.Error(), "bootstrap credential") {
		t.Fatalf("error = %v, want it to mention bootstrap credential", err)
	}
}

// TestDeleteRefusesASMSecretKeyBootstrapRef covers asm's static-keys
// auth: the secret_key_ref (defaulting to AWS_SECRET_ACCESS_KEY) must
// be refused on delete while asm is configured for keys auth.
func TestDeleteRefusesASMSecretKeyBootstrapRef(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	secretKeyRef := "TEST_DELETE_BOOTSTRAP_ASMKEY_" + t.Name()

	origCfg, err := s.GetBackendConfig(ctx, "asm")
	if err != nil {
		t.Fatalf("GetBackendConfig: %v", err)
	}
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() {
		_, _ = db.Exec(ctx, `DELETE FROM secrets WHERE ref_name = $1`, secretKeyRef)
		if string(origCfg) != "{}" {
			if _, err := s.SetBackendConfig(ctx, "asm", origCfg); err != nil {
				t.Errorf("restore asm config: %v", err)
			}
		} else if err := s.DeleteBackendConfig(ctx, "asm"); err != nil {
			t.Errorf("delete asm config: %v", err)
		}
	}()

	cfg := `{"auth":"keys","access_key_id":"AKIAEXAMPLE","secret_key_ref":"` + secretKeyRef + `"}`
	if _, err := s.SetBackendConfig(ctx, "asm", []byte(cfg)); err != nil {
		t.Fatalf("SetBackendConfig: %v", err)
	}
	if err := s.SetDB(ctx, secretKeyRef, "secret-key-x"); err != nil {
		t.Fatalf("SetDB: %v", err)
	}

	err = s.Delete(ctx, secretKeyRef)
	if err == nil {
		t.Fatal("Delete accepted removing the asm secret_key_ref while asm keys auth is configured")
	}
	if !strings.Contains(err.Error(), "bootstrap credential") {
		t.Fatalf("error = %v, want it to mention bootstrap credential", err)
	}
}

// TestDeleteAllowsOrdinaryRefWhenNoBackendConfigured pins that the new
// guard only ever blocks an actual bootstrap ref: with no external
// backend configured, bootstrapRefs is empty and any ordinary secret
// deletes exactly as before.
func TestDeleteAllowsOrdinaryRefWhenNoBackendConfigured(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	ref := "TEST_DELETE_ORDINARY_" + t.Name()

	if err := s.SetDB(ctx, ref, "sk-ordinary"); err != nil {
		t.Fatalf("SetDB: %v", err)
	}
	if err := s.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if configured, _, err := s.Status(ctx, ref); err != nil || configured {
		t.Fatalf("Status after Delete: configured=%v err=%v", configured, err)
	}
}

// TestBootstrapRefsExported pins BootstrapRefs (the exported wrapper
// admin.ListSecrets uses to flag "system" refs) against a configured
// vault backend with a non-default token_ref name.
func TestBootstrapRefsExported(t *testing.T) {
	s := integrationStore(t)
	ctx := t.Context()
	tokenRef := "TEST_BOOTSTRAPREFS_CUSTOM_TOKEN_" + t.Name()

	srv, _ := fakeVaultKV(t)
	defer srv.Close()
	restore := setUpVaultDefault(t, s, srv, tokenRef)
	defer restore()
	db, err := s.db.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() {
		_, _ = db.Exec(ctx, `DELETE FROM secrets WHERE ref_name = $1`, tokenRef)
	}()

	refs, err := s.BootstrapRefs(ctx)
	if err != nil {
		t.Fatalf("BootstrapRefs: %v", err)
	}
	if refs[tokenRef] != "vault" {
		t.Fatalf("BootstrapRefs[%q] = %q, want vault", tokenRef, refs[tokenRef])
	}
	if refs["SOME_OTHER_REF"] != "" {
		t.Fatalf("BootstrapRefs flagged an unrelated ref: %q", refs["SOME_OTHER_REF"])
	}
}
