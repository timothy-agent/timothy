//go:build integration

package admin

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/migrations"
)

const adminMarker = "itest-admin-"

func testAdmin(t *testing.T) (*Admin, *router.Store, *pgpool.Pool) {
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
		_, _ = db.Exec(context.Background(),
			"DELETE FROM task_routes WHERE task_category LIKE $1 || '%'", adminMarker)
		_, _ = db.Exec(context.Background(),
			"DELETE FROM providers WHERE name LIKE $1 || '%'", adminMarker)
		_, _ = db.Exec(context.Background(),
			"DELETE FROM admin_audit WHERE entity_id LIKE $1 || '%' OR after::text LIKE '%' || $1 || '%'", adminMarker)
		_, _ = db.Exec(context.Background(),
			"DELETE FROM cost_ledger WHERE provider LIKE $1 || '%'", adminMarker)
	})

	store := router.NewStore(pool, func(string) string { return "resolved" }, log)
	if err := store.Load(ctx); err != nil {
		t.Fatalf("store load: %v", err)
	}
	return New(pool, store, ledger.New(pool, log), log), store, pool
}

func TestProviderCRUDAuditsAndReloads(t *testing.T) {
	adm, store, pool := testAdmin(t)
	ctx := t.Context()

	name := adminMarker + "crud"
	id, err := adm.Create(ctx, Provider{
		Name: name, Kind: "api", Driver: "openaicompat",
		BaseURL: "https://example.invalid/v1", DefaultModel: "m1",
		Models:        []router.ModelInfo{{ID: "m1", Capabilities: []string{"chat"}}},
		CredentialRef: "SOME_ENV_NAME",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The serving snapshot reloaded without any restart.
	if _, ok := store.Snapshot().Provider(name); !ok {
		t.Fatal("created provider missing from the reloaded snapshot")
	}

	enabled := true
	if err := adm.Patch(ctx, id, ProviderPatch{Enabled: &enabled}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	list, err := adm.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var got *Provider
	for i := range list {
		if list[i].ID == id {
			got = &list[i]
		}
	}
	if got == nil || !got.Enabled {
		t.Fatalf("patched provider = %+v, want enabled", got)
	}

	// Route pointing at it blocks deletion while enabled.
	if err := adm.PatchRoute(ctx, seedRoute(t, pool, adminMarker+"cat", id), RoutePatch{}); err != nil {
		t.Fatalf("PatchRoute noop: %v", err)
	}
	if err := adm.Delete(ctx, id); err == nil || !strings.Contains(err.Error(), "referenced") {
		t.Fatalf("Delete while routed = %v, want in-use refusal", err)
	}

	// Disable the route, delete goes through, audit trail is complete.
	off := false
	if err := adm.PatchRoute(ctx, adminMarker+"cat", RoutePatch{Enabled: &off}); err != nil {
		t.Fatalf("disable route: %v", err)
	}
	if err := adm.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	db, _ := pool.Get()
	var actions []string
	rows, err := db.Query(ctx, `SELECT action FROM admin_audit WHERE entity_id = $1 ORDER BY ts`, id)
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	if len(actions) < 3 || actions[0] != "create" || actions[len(actions)-1] != "delete" {
		t.Fatalf("audit actions = %v, want create…delete", actions)
	}
}

// seedRoute inserts an enabled route whose chain references the
// provider and returns the category.
func seedRoute(t *testing.T, pool *pgpool.Pool, category, providerID string) string {
	t.Helper()
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_, err = db.Exec(t.Context(), `INSERT INTO task_routes (task_category, chain, enabled)
		VALUES ($1, jsonb_build_array(jsonb_build_object('provider_id', $2::text, 'model', 'm1')), true)
		ON CONFLICT (task_category) DO UPDATE SET chain = EXCLUDED.chain, enabled = true`,
		category, providerID)
	if err != nil {
		t.Fatalf("seed route: %v", err)
	}
	return category
}

func TestValidationRefusesSecretsAndUnknowns(t *testing.T) {
	adm, _, _ := testAdmin(t)
	ctx := t.Context()

	cases := []Provider{
		{Name: adminMarker + "v1", Kind: "api", Driver: "openaicompat",
			CredentialRef: "sk-abc def with spaces"}, // secret-looking
		{Name: adminMarker + "v2", Kind: "api", Driver: "made-up"},
		{Name: adminMarker + "v3", Kind: "cli", Driver: "openaicompat"}, // cli later phase
		{Name: "", Kind: "api", Driver: "openaicompat"},
	}
	for _, p := range cases {
		if _, err := adm.Create(ctx, p); err == nil {
			t.Fatalf("Create(%+v) succeeded, want validation error", p)
		}
	}
}

func TestRoutePatchValidatesProviderRefs(t *testing.T) {
	adm, _, pool := testAdmin(t)
	ctx := t.Context()

	id, err := adm.Create(ctx, Provider{
		Name: adminMarker + "route", Kind: "api", Driver: "openaicompat",
		BaseURL: "https://example.invalid/v1", DefaultModel: "m1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cat := seedRoute(t, pool, adminMarker+"rp", id)

	bogus := []router.ChainEntry{{ProviderID: "00000000-0000-4000-8000-000000000000", Model: "x"}}
	if err := adm.PatchRoute(ctx, cat, RoutePatch{Chain: &bogus}); err == nil {
		t.Fatal("chain with unknown provider id must refuse")
	}

	good := []router.ChainEntry{{ProviderID: id, Model: "m2"}, {ProviderID: id, Model: "m1"}}
	if err := adm.PatchRoute(ctx, cat, RoutePatch{Chain: &good}); err != nil {
		t.Fatalf("valid chain patch: %v", err)
	}
	routes, err := adm.Routes(ctx)
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	for _, r := range routes {
		if r.TaskCategory == cat {
			if len(r.Chain) != 2 || r.Chain[0].Model != "m2" {
				t.Fatalf("chain = %+v, want reordered [m2 m1]", r.Chain)
			}
			return
		}
	}
	t.Fatal("route missing")
}

func TestConnectionProbeBooksAsTestOnly(t *testing.T) {
	adm, _, pool := testAdmin(t)
	ctx := t.Context()

	// Unreachable base URL: the probe must fail honestly, land in the
	// ledger as purpose='test', and stay out of usage aggregates.
	id, err := adm.Create(ctx, Provider{
		Name: adminMarker + "probe", Kind: "api", Driver: "openaicompat",
		BaseURL: "http://127.0.0.1:1/v1", DefaultModel: "m1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := adm.Test(ctx, id, "")
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if res.OK || res.Detail == "" {
		t.Fatalf("probe against a dead endpoint = %+v, want honest failure", res)
	}

	db, _ := pool.Get()
	var purpose string
	if err := db.QueryRow(ctx, `SELECT COALESCE(purpose, '') FROM cost_ledger
		WHERE provider = $1 ORDER BY ts DESC LIMIT 1`, adminMarker+"probe").Scan(&purpose); err != nil {
		t.Fatalf("ledger row: %v", err)
	}
	if purpose != "test" {
		t.Fatalf("probe purpose = %q, want test", purpose)
	}

	agg := ledger.NewAggregator(pool)
	points, err := agg.Series(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), "day", "provider")
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	for _, p := range points {
		if p.Group == adminMarker+"probe" {
			t.Fatalf("test probe leaked into usage series: %+v", p)
		}
	}
}
