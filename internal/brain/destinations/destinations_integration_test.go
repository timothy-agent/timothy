//go:build integration

package destinations

import (
	"context"
	"encoding/json"
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

const marker = "itest-dest-"

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
	if _, err := db.Exec(ctx, "DELETE FROM destinations WHERE name LIKE $1 || '%'", marker); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		_, _ = conn.Exec(cctx, "DELETE FROM destinations WHERE name LIKE $1 || '%'", marker)
	})
	return NewStore(pool, nil, log)
}

func TestStoreCRUDIntegration(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	name := marker + "webhook"

	id, err := store.Create(ctx, Destination{
		Name: name, Kind: "webhook", Enabled: true,
		Config: json.RawMessage(`{"url":"https://example.com/hook","format":"json"}`),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != name || got.Kind != "webhook" || !got.Enabled {
		t.Fatalf("Get returned unexpected row: %+v", got)
	}

	rows, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("List did not include the created destination")
	}

	enabled, err := store.EnabledByID(ctx, id)
	if err != nil || !enabled {
		t.Fatalf("EnabledByID = %v, %v; want true, nil", enabled, err)
	}

	disabled := false
	if err := store.Patch(ctx, id, Patch{Enabled: &disabled}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	enabled, err = store.EnabledByID(ctx, id)
	if err != nil || enabled {
		t.Fatalf("EnabledByID after disable = %v, %v; want false, nil", enabled, err)
	}

	if err := store.Delete(ctx, id, nil, nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, id); err == nil {
		t.Fatal("Get after Delete: expected error, got nil")
	}
}

func TestStoreCreateRejectsUnknownConnector(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()

	_, err := store.Create(ctx, Destination{
		Name: marker + "email", Kind: "email", Enabled: true,
		Config: json.RawMessage(`{"connector_id":"00000000-0000-0000-0000-000000000000","to":"ops@example.com"}`),
	})
	if err == nil {
		t.Fatal("expected error creating an email destination with no connectors configured")
	}
}

// Both repo kinds carry branch_pattern/commit_style and validate them
// the same way, so both must resolve a policy: bitbucket rows were
// accepted and then ignored (issue #787). Rows are inserted directly
// because validate's connector checks are not what this exercises.
func TestStoreRepoPolicyCoversBothRepoKinds(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	db, err := store.db.Get()
	if err != nil {
		t.Fatalf("db: %v", err)
	}

	insert := func(kind, cfg string) string {
		t.Helper()
		var id string
		if err := db.QueryRow(ctx, `INSERT INTO destinations (name, kind, config, enabled)
			VALUES ($1, $2, $3, true) RETURNING id`, marker+kind+"-policy", kind, cfg).Scan(&id); err != nil {
			t.Fatalf("insert %s: %v", kind, err)
		}
		return id
	}

	for _, kind := range []string{"github", "bitbucket"} {
		t.Run(kind, func(t *testing.T) {
			id := insert(kind, `{"branch_pattern":"bb/{slug}","commit_style":"plain"}`)
			policy, ok, err := store.RepoPolicy(ctx, id)
			if err != nil {
				t.Fatalf("RepoPolicy: %v", err)
			}
			if !ok {
				t.Fatalf("RepoPolicy(%s) ok = false, want true: the row's git policy is ignored", kind)
			}
			if policy.BranchPattern != "bb/{slug}" || policy.CommitStyle != "plain" {
				t.Fatalf("RepoPolicy(%s) = %+v, want branch bb/{slug} and style plain", kind, policy)
			}
		})
	}

	t.Run("non-repo kind still reports not ok", func(t *testing.T) {
		id := insert("webhook", `{"url":"https://example.com/hook","format":"json"}`)
		if _, ok, err := store.RepoPolicy(ctx, id); err != nil || ok {
			t.Fatalf("RepoPolicy(webhook) = ok %v, err %v; want false, nil", ok, err)
		}
	})
}
