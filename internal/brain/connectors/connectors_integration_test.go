//go:build integration

package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const marker = "itest-conn-"

func testStore(t *testing.T) (*Store, *pgpool.Pool) {
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
	type execer interface {
		Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	}
	sweep := func(ctx context.Context, db execer) {
		_, _ = db.Exec(ctx, "DELETE FROM connectors WHERE name LIKE $1 || '%'", marker)
		_, _ = db.Exec(ctx, `DELETE FROM admin_audit WHERE entity = 'connector'
			AND (after::text LIKE '%' || $1 || '%' OR before::text LIKE '%' || $1 || '%')`, marker)
	}
	sweep(ctx, db)
	t.Cleanup(func() {
		// The pool dies with t.Context(), which is canceled before
		// cleanups run — sweep over a fresh one-shot connection.
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		sweep(cctx, conn)
	})
	return NewStore(pool, log), pool
}

func TestConnectorCRUDAuditsAndNotifies(t *testing.T) {
	store, pool := testStore(t)
	ctx := t.Context()
	name := marker + "github"

	changes := 0
	store.SetOnChange(func(context.Context) { changes++ })

	id, err := store.Create(ctx, Connector{
		Name: name, Kind: "mcp",
		Config:        json.RawMessage(`{"endpoint":"https://api.example/mcp"}`),
		CredentialRef: "GITHUB_MCP_TOKEN",
		Sensitive:     true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if changes != 1 {
		t.Fatalf("changes after create = %d, want 1", changes)
	}
	if created, err := store.Get(ctx, id); err != nil || !created.Sensitive {
		t.Fatalf("Get after create: sensitive = %v, err = %v, want true", created.Sensitive, err)
	}

	// Duplicate name refused by the unique constraint.
	if _, err := store.Create(ctx, Connector{Name: name, Kind: "mcp"}); err == nil {
		t.Fatal("duplicate name accepted")
	}

	enabled := true
	sensitive := false
	if err := store.Patch(ctx, id, Patch{Enabled: &enabled, Sensitive: &sensitive}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	list, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var got *Connector
	for i := range list {
		if list[i].ID == id {
			got = &list[i]
		}
	}
	if got == nil || !got.Enabled || got.Kind != "mcp" || got.Sensitive {
		t.Fatalf("patched connector = %+v, want enabled mcp not-sensitive", got)
	}

	// Ref validation holds on patch too.
	bad := "has a space"
	if err := store.Patch(ctx, id, Patch{CredentialRef: &bad}); err == nil {
		t.Fatal("secret-looking credential_ref accepted on patch")
	}

	if err := store.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, id); err == nil {
		t.Fatal("Get after delete: want ErrNotFound")
	}
	if changes != 3 { // create + patch + delete; rejected patch must not fire
		t.Fatalf("changes = %d, want 3", changes)
	}

	// Every mutation audited under entity='connector'.
	db, _ := pool.Get()
	var audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM admin_audit
		WHERE entity = 'connector' AND entity_id = $1`, id).Scan(&audits); err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if audits != 3 {
		t.Fatalf("audit rows = %d, want 3 (create, update, delete)", audits)
	}
}

func TestConnectorPatchRename(t *testing.T) {
	store, _ := testStore(t)
	ctx := t.Context()

	name := marker + "rename-a"
	id, err := store.Create(ctx, Connector{Name: name, Kind: "mcp"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	other := marker + "rename-b"
	otherID, err := store.Create(ctx, Connector{Name: other, Kind: "mcp"})
	if err != nil {
		t.Fatalf("Create other: %v", err)
	}

	t.Run("valid slug renames", func(t *testing.T) {
		newName := marker + "renamed"
		if err := store.Patch(ctx, id, Patch{Name: &newName}); err != nil {
			t.Fatalf("Patch: %v", err)
		}
		got, err := store.Get(ctx, id)
		if err != nil || got.Name != newName {
			t.Fatalf("Get after rename = %+v, err = %v, want name %s", got, err, newName)
		}
	})

	t.Run("invalid slug rejected", func(t *testing.T) {
		bad := "Has Spaces"
		if err := store.Patch(ctx, id, Patch{Name: &bad}); err == nil {
			t.Fatal("invalid slug accepted")
		}
	})

	t.Run("duplicate name rejected", func(t *testing.T) {
		if err := store.Patch(ctx, id, Patch{Name: &other}); !errors.Is(err, ErrNameConflict) {
			t.Fatalf("Patch error = %v, want ErrNameConflict", err)
		}
	})

	if err := store.Delete(ctx, otherID); err != nil {
		t.Fatalf("Delete other: %v", err)
	}
}

func TestManagerReloadFromDB(t *testing.T) {
	store, _ := testStore(t)
	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	mgr := NewManager(store, func(context.Context, string) (string, error) { return "resolved", nil }, log)
	mgr.RegisterBuilder("mcp", func(_ context.Context, c Connector, _ Resolve) (Source, error) {
		return nopSource{}, nil
	})

	// Create-enabled fires the change hook, which reloads the manager.
	name := marker + "live"
	if _, err := store.Create(ctx, Connector{Name: name, Kind: "mcp", Enabled: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	found := false
	for _, n := range mgr.Names() {
		if n == name {
			found = true
		}
	}
	if !found {
		t.Fatalf("Names = %v, want %s present after create", mgr.Names(), name)
	}
}

type nopSource struct{}

func (nopSource) Tools() []*tools.Tool        { return nil }
func (nopSource) Test(context.Context) error  { return nil }
func (nopSource) Close() error                { return nil }
