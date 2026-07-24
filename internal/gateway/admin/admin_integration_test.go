//go:build integration

package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/internal/secretstore"
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
	// Sweep runs at setup AND teardown: a killed run never executes
	// cleanups, and its leftovers would fail every later run (unique
	// name collisions, accumulated audit rows) — and pollute a shared
	// dev database with fixture rows.
	type execer interface {
		Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	}
	sweep := func(ctx context.Context, db execer) {
		_, _ = db.Exec(ctx,
			"DELETE FROM routes WHERE name LIKE $1 || '%'", adminMarker)
		_, _ = db.Exec(ctx,
			"DELETE FROM providers WHERE name LIKE $1 || '%'", adminMarker)
		// budget, secret-backend, and route-bootstrap audit rows carry no
		// fixture marker (their payloads are bare scope/backend names or
		// a provider UUID, and route-bootstrap's restore write re-records
		// the real chain), so those entities sweep whole — a few lost
		// trail rows about real budget/backend/route changes beat fixture
		// rows accumulating forever.
		_, _ = db.Exec(ctx,
			`DELETE FROM admin_audit
			 WHERE entity IN ('budget', 'secret_backend', 'secret_backend_default')
			 OR (entity = 'route' AND action = 'bootstrap')
			 OR entity_id LIKE $1 || '%'
			 OR before::text LIKE '%' || $1 || '%' OR after::text LIKE '%' || $1 || '%'`, adminMarker)
		_, _ = db.Exec(ctx,
			"DELETE FROM cost_ledger WHERE provider LIKE $1 || '%'", adminMarker)
		_, _ = db.Exec(ctx, "DELETE FROM secrets WHERE ref_name LIKE $1 || '%'", adminMarker)
		// Any test creating a chat/embeddings-capable provider now
		// bootstraps it into default/summarize/embedding as a side
		// effect (D-033 follow-up); deleting the provider row leaves a
		// dangling chain entry the shared routes never asked for. Strip
		// any entry whose provider_id no longer exists — cheaper and
		// more robust than every fixture test knowing about bootstrap.
		_, _ = db.Exec(ctx, `
			UPDATE routes SET chain = (
				SELECT COALESCE(jsonb_agg(e), '[]'::jsonb)
				FROM jsonb_array_elements(chain) e
				WHERE EXISTS (SELECT 1 FROM providers WHERE id = (e->>'provider_id')::uuid)
			)
			WHERE name IN ('default', 'summarize', 'embedding')
			  AND chain <> '[]'::jsonb`)
	}
	sweep(ctx, db)
	t.Cleanup(func() {
		// The pool dies with t.Context(), which is canceled before
		// cleanups run — a sweep through it fails silently and leaves
		// fixture rows behind. Sweep over a fresh one-shot connection.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			t.Logf("teardown sweep skipped: %v", err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		sweep(ctx, conn)
	})

	store := router.NewStore(pool, func(string) string { return "resolved" }, log)
	if err := store.Load(ctx); err != nil {
		t.Fatalf("store load: %v", err)
	}
	masterKey := make([]byte, 32)
	secrets, err := secretstore.New(pool, masterKey)
	if err != nil {
		t.Fatalf("secretstore.New: %v", err)
	}
	return New(pool, store, ledger.New(pool, log), ledger.NewBudgetStore(pool), secrets, log), store, pool
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
	// This provider declares a chat-capable model, so Create also
	// bootstrapped it into the shared default/summarize routes and
	// (D-033 follow-up) enabled them — a second, unrelated enabled
	// reference Delete's guard sees just as validly as the scoped one
	// above. Strip it the same way the shared sweep does for
	// leftovers, but inline: this test's own Delete call needs it gone
	// NOW, not at teardown.
	db, _ := pool.Get()
	if _, err := db.Exec(ctx, `
		UPDATE routes SET chain = (
			SELECT COALESCE(jsonb_agg(e), '[]'::jsonb)
			FROM jsonb_array_elements(chain) e
			WHERE e->>'provider_id' <> $1
		)
		WHERE name IN ('default', 'summarize', 'embedding')`, id); err != nil {
		t.Fatalf("strip bootstrapped chain refs: %v", err)
	}
	if err := adm.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

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

// SetSecretValue routes through the store-wide default backend: the
// built-in store encrypts the value itself, an external default
// records the value as that backend's reference.
func TestSetSecretValueFollowsDefaultBackend(t *testing.T) {
	adm, _, _ := testAdmin(t)
	ctx := t.Context()

	ref := adminMarker + "SECRET_DB"
	if err := adm.SetSecretValue(ctx, ref, "sk-plain"); err != nil {
		t.Fatalf("SetSecretValue: %v", err)
	}
	if configured, backend, err := adm.SecretStatus(ctx, ref); err != nil || !configured || backend != "db" {
		t.Fatalf("Status = %v %q %v, want configured via db", configured, backend, err)
	}

	// Shared table: restore the pre-test vault config and default flag.
	// Defers run while t.Context() is still alive; t.Cleanup would not.
	origCfg, err := adm.SecretBackendConfig(ctx, "vault")
	if err != nil {
		t.Fatalf("SecretBackendConfig: %v", err)
	}
	defer func() {
		if string(origCfg) != "{}" {
			if err := adm.SetSecretBackendConfig(ctx, "vault", origCfg); err != nil {
				t.Errorf("restore vault config: %v", err)
			}
		} else if err := adm.DeleteSecretBackendConfig(ctx, "vault"); err != nil {
			t.Errorf("remove test vault config: %v", err)
		}
		if err := adm.SetDefaultSecretBackend(ctx, "db"); err != nil {
			t.Errorf("restore default backend: %v", err)
		}
	}()

	if err := adm.SetSecretBackendConfig(ctx, "vault", []byte(`{"address":"http://127.0.0.1:1"}`)); err != nil {
		t.Fatalf("SetSecretBackendConfig: %v", err)
	}
	if err := adm.SetDefaultSecretBackend(ctx, "vault"); err != nil {
		t.Fatalf("SetDefaultSecretBackend: %v", err)
	}
	extRef := adminMarker + "SECRET_VAULT"
	if err := adm.SetSecretValue(ctx, extRef, "timothy/key#api_key"); err != nil {
		t.Fatalf("SetSecretValue external: %v", err)
	}
	if configured, backend, err := adm.SecretStatus(ctx, extRef); err != nil || !configured || backend != "vault" {
		t.Fatalf("Status = %v %q %v, want configured via vault", configured, backend, err)
	}
}

// A provider created without models/headers, and rows that already
// hold jsonb null (written before jsonOr guarded typed nils), must
// come back as [] / {} — a null models array crashes the settings UI.
func TestProviderNilModelsRoundTripsAsEmpty(t *testing.T) {
	adm, _, pool := testAdmin(t)
	ctx := t.Context()

	id, err := adm.Create(ctx, Provider{
		Name: adminMarker + "nilmodels", Kind: "api", Driver: "bedrock",
		BaseURL: "us-east-1", CredentialRef: "SOME_ENV_NAME",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	db, _ := pool.Get()
	var raw string
	if err := db.QueryRow(ctx, `SELECT models::text FROM providers WHERE id = $1`, id).Scan(&raw); err != nil {
		t.Fatalf("models query: %v", err)
	}
	if raw != "[]" {
		t.Fatalf("stored models = %s, want []", raw)
	}

	// Simulate a pre-fix row: jsonb null in both columns.
	if _, err := db.Exec(ctx, `UPDATE providers SET models = 'null', headers = 'null' WHERE id = $1`, id); err != nil {
		t.Fatalf("force null: %v", err)
	}
	list, err := adm.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, p := range list {
		if p.ID != id {
			continue
		}
		if p.Models == nil || p.Headers == nil {
			t.Fatalf("List returned nil models/headers: %+v", p)
		}
		return
	}
	t.Fatal("created provider missing from List")
}

// seedRoute inserts an enabled route whose chain references the
// provider and returns the category.
func seedRoute(t *testing.T, pool *pgpool.Pool, category, providerID string) string {
	t.Helper()
	db, err := pool.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_, err = db.Exec(t.Context(), `INSERT INTO routes (name, chain, enabled)
		VALUES ($1, jsonb_build_array(jsonb_build_object('provider_id', $2::text, 'model', 'm1')), true)
		ON CONFLICT (name) DO UPDATE SET chain = EXCLUDED.chain, enabled = true`,
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
		if r.Name == cat {
			if len(r.Chain) != 2 || r.Chain[0].Model != "m2" {
				t.Fatalf("chain = %+v, want reordered [m2 m1]", r.Chain)
			}
			// PatchRoute reloaded the snapshot, so the enabled route
			// carries the router's resolved view.
			if len(r.Resolved) != 2 || !r.Resolved[0].Usable {
				t.Fatalf("resolved = %+v, want 2 usable entries", r.Resolved)
			}
			if r.Serving == nil || r.Serving.Model != "m2" {
				t.Fatalf("serving = %+v, want m2", r.Serving)
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
	// Probe audit rows carry only the provider UUID (no marker), so the
	// shared sweep cannot match them — delete by the id captured here.
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ccancel()
		conn, err := pgx.Connect(cctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer func() { _ = conn.Close(cctx) }()
		if _, err := conn.Exec(cctx, "DELETE FROM admin_audit WHERE entity_id = $1", id); err != nil {
			t.Errorf("cleanup probe audit rows: %v", err)
		}
	})
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

func TestPatchBudgetValidatesAndAudits(t *testing.T) {
	adm, _, pool := testAdmin(t)
	ctx := t.Context()
	db, _ := pool.Get()

	// The day/month budget scopes are shared state (a dev database may
	// hold real limits): start from a clean slate, restore afterwards.
	// A defer, not t.Cleanup — it runs while the pool is still alive.
	// Audit rows are the shared sweep's job (entity = 'budget').
	orig, err := ledger.NewBudgetStore(pool).Limits(ctx)
	if err != nil {
		t.Fatalf("limits (orig): %v", err)
	}
	if _, err := db.Exec(ctx, "DELETE FROM spend_budgets"); err != nil {
		t.Fatalf("clear budgets: %v", err)
	}
	defer func() {
		if _, err := db.Exec(ctx, "DELETE FROM spend_budgets"); err != nil {
			t.Errorf("sweep budgets: %v", err)
		}
		s := ledger.NewBudgetStore(pool)
		if err := s.Set(ctx, "day", orig.Day); err != nil {
			t.Errorf("restore day budget: %v", err)
		}
		if err := s.Set(ctx, "month", orig.Month); err != nil {
			t.Errorf("restore month budget: %v", err)
		}
	}()

	// Any invalid key rejects the whole patch before writes.
	limit := 5.0
	if err := adm.PatchBudget(ctx, map[string]*float64{"day": &limit, "week": &limit}); err == nil {
		t.Fatal("unknown scope accepted")
	}
	var count int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM spend_budgets`).Scan(&count)
	if count != 0 {
		t.Fatalf("partial write: %d rows after rejected patch", count)
	}

	// Valid patch writes both windows and audits once.
	month := 100.0
	if err := adm.PatchBudget(ctx, map[string]*float64{"day": &limit, "month": &month}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	limits, err := ledger.NewBudgetStore(pool).Limits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	if limits.Day == nil || *limits.Day != limit || limits.Month == nil || *limits.Month != month {
		t.Fatalf("limits = %+v, want day=%v month=%v", limits, limit, month)
	}
	var audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM admin_audit
		WHERE entity = 'budget' AND action = 'update'`).Scan(&audits); err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if audits != 1 {
		t.Fatalf("audit rows = %d, want 1", audits)
	}
}

func TestSecretSetDeleteAuditsAndReloads(t *testing.T) {
	adm, store, pool := testAdmin(t)
	ctx := t.Context()
	db, _ := pool.Get()
	ref := adminMarker + "secret"

	if configured, _, err := adm.SecretStatus(ctx, ref); err != nil || configured {
		t.Fatalf("SecretStatus before Set: configured=%v err=%v", configured, err)
	}

	before := store.Snapshot()
	if err := adm.SetSecret(ctx, ref, "sk-live-value"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	if configured, backend, err := adm.SecretStatus(ctx, ref); err != nil || !configured || backend != "db" {
		t.Fatalf("SecretStatus after Set: configured=%v backend=%q err=%v", configured, backend, err)
	}
	if store.Snapshot() == before {
		t.Fatal("SetSecret did not trigger a snapshot reload")
	}

	var audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM admin_audit
		WHERE entity = 'secret' AND entity_id = $1 AND action = 'set'`, ref).Scan(&audits); err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if audits != 1 {
		t.Fatalf("audit rows = %d, want 1", audits)
	}

	if err := adm.DeleteSecret(ctx, ref); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if configured, _, err := adm.SecretStatus(ctx, ref); err != nil || configured {
		t.Fatalf("SecretStatus after Delete: configured=%v err=%v", configured, err)
	}
}

// TestCreateBootstrapsFixedRoutes pins the Create-time wiring of
// router.BootstrapChain: default/summarize get the cheapest chat-
// capable model, embedding gets the cheapest embeddings-capable one,
// and re-creating a provider with the same model does not duplicate
// the chain entry. default/summarize/embedding are shared, real
// routes (not marker-scoped) — save and restore their chains so the
// test never leaves the dev database's routing altered.
func TestCreateBootstrapsFixedRoutes(t *testing.T) {
	adm, _, pool := testAdmin(t)
	ctx := t.Context()
	db, _ := pool.Get()

	type saved struct {
		chain   []byte
		enabled bool
	}
	origs := map[string]saved{}
	for _, name := range []string{"default", "summarize", "embedding"} {
		var s saved
		if err := db.QueryRow(ctx, `SELECT chain, enabled FROM routes WHERE name = $1`, name).Scan(&s.chain, &s.enabled); err != nil {
			t.Fatalf("read %s route: %v", name, err)
		}
		origs[name] = s
	}
	defer func() {
		for name, s := range origs {
			if _, err := db.Exec(ctx, `UPDATE routes SET chain = $2, enabled = $3 WHERE name = $1`, name, s.chain, s.enabled); err != nil {
				t.Errorf("restore %s route: %v", name, err)
			}
		}
	}()

	// Force every fixed route disabled first — a seeded (was-empty)
	// chain must flip enabled on; an appended (already-had-a-chain)
	// fallback must leave it exactly as found.
	for _, name := range []string{"default", "summarize", "embedding"} {
		if _, err := db.Exec(ctx, `UPDATE routes SET chain = '[]', enabled = false WHERE name = $1`, name); err != nil {
			t.Fatalf("reset %s route: %v", name, err)
		}
	}

	id, err := adm.Create(ctx, Provider{
		Name: adminMarker + "bootstrap", Kind: "api", Driver: "openaicompat",
		BaseURL: "https://example.invalid/v1", DefaultModel: "chat-cheap",
		Models: []router.ModelInfo{
			{ID: "chat-cheap", Capabilities: []string{"chat"}, Prices: &router.ModelPrices{InputPerMTok: 1}},
			{ID: "chat-pricey", Capabilities: []string{"chat"}, Prices: &router.ModelPrices{InputPerMTok: 9}},
			{ID: "embed-only", Capabilities: []string{"embeddings"}, Prices: &router.ModelPrices{InputPerMTok: 0.1}},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	routes, err := adm.Routes(ctx)
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	byName := map[string]Route{}
	for _, r := range routes {
		byName[r.Name] = r
	}
	for _, tc := range []struct{ route, wantModel string }{
		{"default", "chat-cheap"},
		{"summarize", "chat-cheap"},
		{"embedding", "embed-only"},
	} {
		chain := byName[tc.route].Chain
		last := chain[len(chain)-1]
		if last.ProviderID != id || last.Model != tc.wantModel {
			t.Fatalf("%s chain tail = %+v, want provider %s model %s", tc.route, last, id, tc.wantModel)
		}
		if !byName[tc.route].Enabled {
			t.Fatalf("%s enabled = false, want true: seeding an empty chain must make the route usable", tc.route)
		}
	}

	// Disable default again (chain now non-empty) to prove the next
	// bootstrap — a fallback append, not a seed — leaves enabled alone
	// instead of silently re-enabling a route an operator turned off.
	if _, err := db.Exec(ctx, `UPDATE routes SET enabled = false WHERE name = 'default'`); err != nil {
		t.Fatalf("disable default: %v", err)
	}

	// A second, distinct provider appends as a further fallback —
	// existing entries (including the one just bootstrapped) untouched.
	before := len(byName["default"].Chain)
	secondID, err := adm.Create(ctx, Provider{
		Name: adminMarker + "bootstrap2", Kind: "api", Driver: "openaicompat",
		BaseURL: "https://example.invalid/v1", DefaultModel: "chat-cheap",
		Models: []router.ModelInfo{{ID: "chat-cheap", Capabilities: []string{"chat"},
			Prices: &router.ModelPrices{InputPerMTok: 1}}},
	})
	if err != nil {
		t.Fatalf("Create second provider: %v", err)
	}
	routes, err = adm.Routes(ctx)
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	for _, r := range routes {
		if r.Name != "default" {
			continue
		}
		if len(r.Chain) != before+1 {
			t.Fatalf("default chain length = %d, want %d (existing + one fallback)", len(r.Chain), before+1)
		}
		last := r.Chain[len(r.Chain)-1]
		if last.ProviderID != secondID || last.Model != "chat-cheap" {
			t.Fatalf("default chain tail = %+v, want second provider's chat-cheap appended last", last)
		}
		if r.Enabled {
			t.Fatalf("default enabled = true, want false: appending a fallback must not override an operator's disable")
		}
	}
}

func TestSecretExternalBackendConfig(t *testing.T) {
	adm, _, _ := testAdmin(t)
	ctx := t.Context()
	ref := adminMarker + "vault-secret"

	// Shared table: restore the pre-test vault config. A defer, not
	// t.Cleanup — it runs while t.Context() and the pool are alive.
	origCfg, err := adm.SecretBackendConfig(ctx, "vault")
	if err != nil {
		t.Fatalf("SecretBackendConfig (orig): %v", err)
	}
	defer func() {
		if string(origCfg) != "{}" {
			if err := adm.SetSecretBackendConfig(ctx, "vault", origCfg); err != nil {
				t.Errorf("restore vault config: %v", err)
			}
		} else if err := adm.DeleteSecretBackendConfig(ctx, "vault"); err != nil {
			t.Errorf("remove test vault config: %v", err)
		}
	}()

	// The fixture address carries the marker so its audit rows match
	// the shared sweep.
	if err := adm.SetSecretBackendConfig(ctx, "vault",
		[]byte(`{"address":"http://`+adminMarker+`vault.invalid:8200","mount":"kv"}`)); err != nil {
		t.Fatalf("SetSecretBackendConfig: %v", err)
	}
	raw, err := adm.SecretBackendConfig(ctx, "vault")
	if err != nil {
		t.Fatalf("SecretBackendConfig: %v", err)
	}
	var cfg map[string]string
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config %s: %v", raw, err)
	}
	want := map[string]string{"address": "http://" + adminMarker + "vault.invalid:8200", "mount": "kv", "token_ref": "VAULT_TOKEN"}
	for k, v := range want {
		if cfg[k] != v {
			t.Errorf("config[%s] = %q, want %q", k, cfg[k], v)
		}
	}

	if err := adm.SetSecretExternal(ctx, ref, "vault", "timothy/anthropic#api_key"); err != nil {
		t.Fatalf("SetSecretExternal: %v", err)
	}
	if configured, backend, err := adm.SecretStatus(ctx, ref); err != nil || !configured || backend != "vault" {
		t.Fatalf("SecretStatus: configured=%v backend=%q err=%v", configured, backend, err)
	}

	if err := adm.SetSecretExternal(ctx, ref, "db", "x"); err == nil {
		t.Fatal("SetSecretExternal with backend db: want error")
	}
	if err := adm.SetSecretExternal(ctx, ref, "vault", ""); err == nil {
		t.Fatal("SetSecretExternal without backend_ref: want error")
	}
}
