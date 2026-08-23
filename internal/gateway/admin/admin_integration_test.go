//go:build integration

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/SumonMSelim/timothy/internal/gateway/catalog"
	"github.com/SumonMSelim/timothy/internal/gateway/ledger"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
	"github.com/SumonMSelim/timothy/internal/platform/migrate"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/internal/secretstore"
	"github.com/SumonMSelim/timothy/migrations"
)

const adminMarker = "itest-admin-"

// chainJSON marshals a chain literal into the *json.RawMessage
// RoutePatch.Chain now expects (D-051 rework: PatchRoute decodes it
// itself so it can also detect a rejected legacy "harness" key).
func chainJSON(t *testing.T, entries []router.ChainEntry) *json.RawMessage {
	t.Helper()
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal chain: %v", err)
	}
	raw := json.RawMessage(b)
	return &raw
}

func testAdmin(t *testing.T) (*Admin, *router.Store, *pgpool.Pool) {
	t.Helper()
	return testAdminWithCatalog(t, catalog.New(slog.New(slog.NewTextHandler(io.Discard, nil))))
}

// testAdminWithCatalog is testAdmin with the model catalog supplied by
// the caller instead of built fresh: router.Store's pricing lookup
// (Snapshot.Prices, behind Test's cost probe) reads an unexported
// field set at construction, so a test needing seeded catalog prices
// on the ROUTER side (not just admin.catalog, which Create/CatalogPrices
// read directly) must pass its fixture in before the store is built.
func testAdminWithCatalog(t *testing.T, cat *catalog.Store) (*Admin, *router.Store, *pgpool.Pool) {
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

	// The bedrock driver JSON-parses the resolved credential at registry
	// build time (D-048), so a non-JSON stub fails every Store.Load
	// whenever any bedrock provider row exists — this package's own
	// bedrock fixture, or a real "AWS Bedrock" row in a shared dev DB.
	store := router.NewStore(pool, func(string) string {
		return `{"access_key_id":"itest","secret_access_key":"itest"}`
	}, cat, log)
	if err := store.Load(ctx); err != nil {
		t.Fatalf("store load: %v", err)
	}
	masterKey := make([]byte, 32)
	secrets, err := secretstore.New(pool, masterKey)
	if err != nil {
		t.Fatalf("secretstore.New: %v", err)
	}
	return New(pool, store, ledger.New(pool, log), ledger.NewBudgetStore(pool), secrets, cat, log), store, pool
}

// waitSnapshot retries reload until the provider reaches the
// serving snapshot: the shared CI database means a concurrent
// package's intentionally-invalid fixture row can make any single
// Store.Load fail (kept-last-good by design, see Admin.reload) —
// retrying rides out that window instead of flaking.
func waitSnapshot(t *testing.T, store *router.Store, name string) {
	t.Helper()
	ctx := t.Context()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = store.Load(ctx)
		if _, ok := store.Snapshot().Provider(name); ok {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("provider %s not in serving snapshot after retry; last Load err: %v", name, lastErr)
}

func TestProviderCRUDAuditsAndReloads(t *testing.T) {
	adm, store, pool := testAdmin(t)
	ctx := t.Context()

	name := adminMarker + "crud"
	id, err := adm.Create(ctx, Provider{
		Name: name, Kind: "api", Driver: "openaicompat",
		BaseURL:       "https://example.invalid/v1",
		DefaultModel:  "m1",
		CredentialRef: "SOME_ENV_NAME",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The serving snapshot reloaded without any restart; a concurrent
	// package's invalid fixture row can transiently fail Store.Load
	// (kept-last-good), so wait it out instead of a one-shot check.
	waitSnapshot(t, store, name)

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

// SetSecretValue routes through the store-wide default backend: with
// db (the seeded default) the built-in store encrypts the value
// itself. The external-default half of this contract is write-through
// and needs a live backend — TestSecretValueWriteThrough covers it
// against a fake KV v2 server.
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
		{Name: adminMarker + "v3", Kind: "cli", Driver: "openaicompat"}, // openaicompat is not a known cli driver
		{Name: "", Kind: "api", Driver: "openaicompat"},
	}
	for _, p := range cases {
		if _, err := adm.Create(ctx, p); err == nil {
			t.Fatalf("Create(%+v) succeeded, want validation error", p)
		}
	}
}

// TestDeleteGuardMatchesHarnessChainEntry confirms the jsonb
// containment check in Delete (chain @> [{"provider_id": id}]) still
// matches a chain entry that carries extra fields — model and, since
// D-051, harness. jsonb array containment tests each stored element as
// a superset of the probe object, so an entry with harness present is
// still found; this pins that behavior rather than assuming it.
func TestDeleteGuardMatchesHarnessChainEntry(t *testing.T) {
	adm, _, pool := testAdmin(t)
	ctx := t.Context()
	db, _ := pool.Get()

	id, err := adm.Create(ctx, Provider{
		Name: adminMarker + "harnessdel", Kind: "cli", Driver: "claude-cli",
		CredentialRef: "subscription",
		Options:       map[string]string{"anthropic_base_url": "http://localhost:9999"},
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	route := adminMarker + "harnessroute"
	_, err = db.Exec(ctx, `INSERT INTO routes (name, chain, enabled)
		VALUES ($1, jsonb_build_array(jsonb_build_object(
			'provider_id', $2::text, 'model', 'claude-sonnet-4', 'harness', 'claude-cli')), true)
		ON CONFLICT (name) DO UPDATE SET chain = EXCLUDED.chain, enabled = true`,
		route, id)
	if err != nil {
		t.Fatalf("seed harness route: %v", err)
	}

	if err := adm.Delete(ctx, id); err == nil || !strings.Contains(err.Error(), "referenced") {
		t.Fatalf("Delete with harness entry referencing it = %v, want in-use refusal", err)
	}

	if _, err := db.Exec(ctx, `UPDATE routes SET enabled = false WHERE name = $1`, route); err != nil {
		t.Fatalf("disable harness route: %v", err)
	}
	if err := adm.Delete(ctx, id); err != nil {
		t.Fatalf("Delete after disabling harness route: %v", err)
	}
}

func TestRoutePatchValidatesProviderRefs(t *testing.T) {
	adm, _, pool := testAdmin(t)
	ctx := t.Context()

	id, err := adm.Create(ctx, Provider{
		Name: adminMarker + "route", Kind: "api", Driver: "openaicompat",
		BaseURL: "https://example.invalid/v1", DefaultModel: "m1",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cat := seedRoute(t, pool, adminMarker+"rp", id)

	bogus := []router.ChainEntry{{ProviderID: "00000000-0000-4000-8000-000000000000", Model: "x"}}
	if err := adm.PatchRoute(ctx, cat, RoutePatch{Chain: chainJSON(t, bogus)}); err == nil {
		t.Fatal("chain with unknown provider id must refuse")
	}

	good := []router.ChainEntry{{ProviderID: id, Model: "m2"}, {ProviderID: id, Model: "m1"}}
	if err := adm.PatchRoute(ctx, cat, RoutePatch{Chain: chainJSON(t, good)}); err != nil {
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

// TestRoutePatchRejectsLegacyHarnessKey covers D-051's rework: harness
// selection moved to the mission column, so a chain entry is pure
// {provider_id, model} again. A write carrying a "harness" key
// (stale UI, hand-edited request, a pre-rework client) must be
// REJECTED with a clear error, never silently dropped — router.
// ChainEntry has no Harness field to decode it into, so PatchRoute
// probes the raw JSON itself to catch this.
func TestRoutePatchRejectsLegacyHarnessKey(t *testing.T) {
	adm, _, pool := testAdmin(t)
	ctx := t.Context()

	anthropicID, err := adm.Create(ctx, Provider{
		Name: adminMarker + "harness-anthropic", Kind: "api", Driver: "anthropic",
		DefaultModel: "sonnet", CredentialRef: "SOME_ENV_NAME", Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create anthropic: %v", err)
	}
	cat := seedRoute(t, pool, adminMarker+"harnesspatch", anthropicID)

	legacy := json.RawMessage(`[{"provider_id":"` + anthropicID + `","model":"sonnet","harness":"claude-cli"}]`)
	if err := adm.PatchRoute(ctx, cat, RoutePatch{Chain: &legacy}); err == nil || !strings.Contains(err.Error(), "harness moved to mission") {
		t.Fatalf("PatchRoute with legacy harness key = %v, want rejection naming the move", err)
	}

	// A plain {provider_id, model} chain (no harness key at all) still
	// writes normally.
	plain := []router.ChainEntry{{ProviderID: anthropicID, Model: "sonnet"}}
	if err := adm.PatchRoute(ctx, cat, RoutePatch{Chain: chainJSON(t, plain)}); err != nil {
		t.Fatalf("plain chain patch: %v", err)
	}
	routes, err := adm.Routes(ctx)
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	for _, r := range routes {
		if r.Name != cat {
			continue
		}
		if len(r.Chain) != 1 || r.Chain[0].ProviderID != anthropicID {
			t.Fatalf("chain = %+v, want the plain entry", r.Chain)
		}
		return
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
	limit := &ledger.BudgetLimit{Amount: 5.0, Currency: "USD"}
	if err := adm.PatchBudget(ctx, map[string]*ledger.BudgetLimit{"day": limit, "week": limit}); err == nil {
		t.Fatal("unknown scope accepted")
	}
	var count int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM spend_budgets`).Scan(&count)
	if count != 0 {
		t.Fatalf("partial write: %d rows after rejected patch", count)
	}

	// Valid patch writes both windows and audits once.
	month := &ledger.BudgetLimit{Amount: 100.0, Currency: "USD"}
	if err := adm.PatchBudget(ctx, map[string]*ledger.BudgetLimit{"day": limit, "month": month}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	limits, err := ledger.NewBudgetStore(pool).Limits(ctx)
	if err != nil {
		t.Fatalf("limits: %v", err)
	}
	if limits.Day == nil || limits.Day.Amount != limit.Amount || limits.Month == nil || limits.Month.Amount != month.Amount {
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

	// BootstrapChain now sources candidate models from the catalog
	// instead of a per-request Models field: seed a fake LiteLLM
	// source covering the three fixture models. The provider's
	// base_url is unrecognized, so CandidateProvidersForRow leaves the
	// search unrestricted — any litellm_provider value matches.
	catSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"chat-cheap": {"litellm_provider": "openaicompat", "mode": "chat", "input_cost_per_token": 0.000001},
			"chat-pricey": {"litellm_provider": "openaicompat", "mode": "chat", "input_cost_per_token": 0.000009},
			"embed-only": {"litellm_provider": "openaicompat", "mode": "embedding", "input_cost_per_token": 0.0000001}
		}`))
	}))
	defer catSrv.Close()
	adm.catalog = catalog.NewWithURL(slog.New(slog.NewTextHandler(io.Discard, nil)), catSrv.URL)
	if _, err := adm.CatalogRefresh(ctx); err != nil {
		t.Fatalf("CatalogRefresh: %v", err)
	}

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

// TestCreateExcludeFromBootstrapSkipsFixedRoutes covers the fix for a
// real incident: a local Ollama provider auto-bootstrapped onto the
// shared "default" route, and a cloud-outage failover silently served
// a mission turn with a below-floor local model. A provider created
// with exclude_from_bootstrap=true must never touch default/summarize/
// embedding's chains, however cheap or capable its models are.
func TestCreateExcludeFromBootstrapSkipsFixedRoutes(t *testing.T) {
	adm, _, pool := testAdmin(t)
	ctx := t.Context()
	db, _ := pool.Get()

	// Seed a catalog entry matching this provider's model so the
	// exclusion actually has a chat-capable candidate to skip — an
	// empty catalog would pass this test vacuously.
	catSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"qwen2.5:7b": {"litellm_provider": "ollama", "mode": "chat", "input_cost_per_token": 0}
		}`))
	}))
	defer catSrv.Close()
	adm.catalog = catalog.NewWithURL(slog.New(slog.NewTextHandler(io.Discard, nil)), catSrv.URL)
	if _, err := adm.CatalogRefresh(ctx); err != nil {
		t.Fatalf("CatalogRefresh: %v", err)
	}

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

	id, err := adm.Create(ctx, Provider{
		Name: adminMarker + "excluded", Kind: "api", Driver: "openaicompat",
		BaseURL:              "http://ollama.invalid:11434",
		DefaultModel:         "qwen2.5:7b",
		ExcludeFromBootstrap: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	routes, err := adm.Routes(ctx)
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	for _, r := range routes {
		for _, entry := range r.Chain {
			if entry.ProviderID == id {
				t.Fatalf("route %s chain = %+v, want excluded provider %s absent from every fixed route", r.Name, r.Chain, id)
			}
		}
	}

	got, err := adm.get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.ExcludeFromBootstrap {
		t.Fatal("ExcludeFromBootstrap round-tripped as false, want true")
	}
}

// TestListSecretsReportsProviderReferents pins ListSecrets' contract:
// every stored ref comes back with timestamps and the names of every
// provider whose credential_ref matches it, an orphaned ref reports an
// empty (never nil, for a clean UI render) referenced_by.
func TestListSecretsReportsProviderReferents(t *testing.T) {
	adm, _, _ := testAdmin(t)
	ctx := t.Context()

	ref := adminMarker + "listed"
	if err := adm.SetSecret(ctx, ref, "sk-live-value"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	orphan := adminMarker + "orphan"
	if err := adm.SetSecret(ctx, orphan, "sk-live-value-2"); err != nil {
		t.Fatalf("SetSecret orphan: %v", err)
	}
	id, err := adm.Create(ctx, Provider{
		Name: adminMarker + "listing-provider", Kind: "api", Driver: "openaicompat",
		BaseURL: "https://example.invalid/v1", DefaultModel: "m1", CredentialRef: ref,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = adm.Delete(ctx, id) })

	refs, err := adm.ListSecrets(ctx)
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	byName := map[string]SecretRef{}
	for _, r := range refs {
		byName[r.RefName] = r
	}
	got, ok := byName[ref]
	if !ok {
		t.Fatalf("ListSecrets missing %s", ref)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("ref %s timestamps = %+v, want both set", ref, got)
	}
	if len(got.ReferencedBy) != 1 || got.ReferencedBy[0] != adminMarker+"listing-provider" {
		t.Fatalf("ref %s referenced_by = %v, want [%s]", ref, got.ReferencedBy, adminMarker+"listing-provider")
	}
	orphanGot, ok := byName[orphan]
	if !ok {
		t.Fatalf("ListSecrets missing %s", orphan)
	}
	if len(orphanGot.ReferencedBy) != 0 {
		t.Fatalf("orphan ref referenced_by = %v, want empty", orphanGot.ReferencedBy)
	}
}

// TestDeleteSecretRefusesWhileProviderReferencesIt pins the guard added
// to DeleteSecret: a provider naming refName as credential_ref blocks
// the delete regardless of the provider's enabled state (a disabled
// provider still owns the credential), and the row survives; deleting
// the provider first lets the secret go.
func TestDeleteSecretRefusesWhileProviderReferencesIt(t *testing.T) {
	adm, _, _ := testAdmin(t)
	ctx := t.Context()

	ref := adminMarker + "guarded"
	if err := adm.SetSecret(ctx, ref, "sk-live-value"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	id, err := adm.Create(ctx, Provider{
		Name: adminMarker + "guard-provider", Kind: "api", Driver: "openaicompat",
		BaseURL: "https://example.invalid/v1", DefaultModel: "m1", CredentialRef: ref,
		Enabled: false,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := adm.DeleteSecret(ctx, ref); err == nil || !strings.Contains(err.Error(), "referenced by provider") {
		t.Fatalf("DeleteSecret while referenced = %v, want in-use refusal naming the provider", err)
	}
	if configured, _, err := adm.SecretStatus(ctx, ref); err != nil || !configured {
		t.Fatalf("SecretStatus after refused delete: configured=%v err=%v, want still configured", configured, err)
	}

	if err := adm.Delete(ctx, id); err != nil {
		t.Fatalf("Delete provider: %v", err)
	}
	if err := adm.DeleteSecret(ctx, ref); err != nil {
		t.Fatalf("DeleteSecret after provider removed: %v", err)
	}
}

// TestDeleteSecretRefusesBootstrapRef pins the delete-side lockout
// guard at the admin layer: with vault configured (default token_ref),
// DeleteSecret must refuse to remove VAULT_TOKEN, and ListSecrets must
// flag it System: true so the UI can hide its delete action up front.
func TestDeleteSecretRefusesBootstrapRef(t *testing.T) {
	adm, _, _ := testAdmin(t)
	ctx := t.Context()

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
		_ = adm.secrets.Delete(ctx, "VAULT_TOKEN")
	}()

	if err := adm.SetSecretBackendConfig(ctx, "vault",
		[]byte(`{"address":"http://`+adminMarker+`vault.invalid:8200","mount":"kv"}`)); err != nil {
		t.Fatalf("SetSecretBackendConfig: %v", err)
	}
	if err := adm.SetSecret(ctx, "VAULT_TOKEN", "vault-tok-x"); err != nil {
		t.Fatalf("SetSecret VAULT_TOKEN: %v", err)
	}

	if err := adm.DeleteSecret(ctx, "VAULT_TOKEN"); err == nil || !strings.Contains(err.Error(), "bootstrap credential") {
		t.Fatalf("DeleteSecret(VAULT_TOKEN) = %v, want a bootstrap-credential refusal", err)
	}
	if configured, _, err := adm.SecretStatus(ctx, "VAULT_TOKEN"); err != nil || !configured {
		t.Fatalf("SecretStatus after refused delete: configured=%v err=%v, want still configured", configured, err)
	}

	refs, err := adm.ListSecrets(ctx)
	if err != nil {
		t.Fatalf("ListSecrets: %v", err)
	}
	var found bool
	for _, r := range refs {
		if r.RefName != "VAULT_TOKEN" {
			continue
		}
		found = true
		if !r.System {
			t.Errorf("ListSecrets VAULT_TOKEN.System = false, want true while vault is configured")
		}
	}
	if !found {
		t.Fatal("ListSecrets did not report VAULT_TOKEN")
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

	// SetSecretValue with backend "db" pinned bypasses the vault
	// default entirely — no need for a live vault to serve this path.
	if err := adm.SetSecret(ctx, ref, "x"); err != nil {
		t.Fatalf("SetSecret (db pin): %v", err)
	}
	if configured, backend, err := adm.SecretStatus(ctx, ref); err != nil || !configured || backend != "db" {
		t.Fatalf("SecretStatus: configured=%v backend=%q err=%v", configured, backend, err)
	}
}

// TestSecretValueWriteThrough drives SetSecretValue against a fake KV
// v2 server with vault as the default backend: the routed Set must
// write the raw value into vault itself, not just record a reference.
func TestSecretValueWriteThrough(t *testing.T) {
	adm, _, _ := testAdmin(t)
	ctx := t.Context()
	ref := adminMarker + "vault-writethrough"

	origCfg, err := adm.SecretBackendConfig(ctx, "vault")
	if err != nil {
		t.Fatalf("SecretBackendConfig (orig): %v", err)
	}
	origDefault, err := adm.secrets.DefaultBackend(ctx)
	if err != nil {
		t.Fatalf("DefaultBackend (orig): %v", err)
	}
	defer func() {
		if string(origCfg) != "{}" {
			if err := adm.SetSecretBackendConfig(ctx, "vault", origCfg); err != nil {
				t.Errorf("restore vault config: %v", err)
			}
		} else if err := adm.DeleteSecretBackendConfig(ctx, "vault"); err != nil {
			t.Errorf("remove test vault config: %v", err)
		}
		if err := adm.SetDefaultSecretBackend(ctx, origDefault); err != nil {
			t.Errorf("restore default backend: %v", err)
		}
	}()

	var wroteValue string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/auth/token/lookup-self":
			w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/kv/data/timothy/"+ref:
			var body struct {
				Data struct {
					Value string `json:"value"`
				} `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			wroteValue = body.Data.Value
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	if err := adm.SetSecretBackendConfig(ctx, "vault",
		[]byte(`{"address":"`+srv.URL+`","mount":"kv","token_ref":"`+ref+`_TOKEN"}`)); err != nil {
		t.Fatalf("SetSecretBackendConfig: %v", err)
	}
	if err := adm.SetSecret(ctx, ref+"_TOKEN", "vault-tok"); err != nil {
		t.Fatalf("SetSecret token: %v", err)
	}
	if err := adm.SetDefaultSecretBackend(ctx, "vault"); err != nil {
		t.Fatalf("SetDefaultSecretBackend: %v", err)
	}

	if err := adm.SetSecretValue(ctx, ref, "sk-live-through"); err != nil {
		t.Fatalf("SetSecretValue: %v", err)
	}
	if wroteValue != "sk-live-through" {
		t.Fatalf("vault received value %q, want sk-live-through", wroteValue)
	}
	if configured, backend, err := adm.SecretStatus(ctx, ref); err != nil || !configured || backend != "vault" {
		t.Fatalf("SecretStatus: configured=%v backend=%q err=%v", configured, backend, err)
	}
}

// TestMigrateSecretAuditsHasNoValue drives MigrateSecret db->vault
// against a fake KV v2 server and checks the audit row records only
// the backend name, never the secret value.
func TestMigrateSecretAuditsHasNoValue(t *testing.T) {
	adm, _, pool := testAdmin(t)
	ctx := t.Context()
	db, _ := pool.Get()
	ref := adminMarker + "migrate-one"

	var wroteValue string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/auth/token/lookup-self":
			w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/kv/data/timothy/"+ref:
			var body struct {
				Data struct {
					Value string `json:"value"`
				} `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			wroteValue = body.Data.Value
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	origCfg, err := adm.SecretBackendConfig(ctx, "vault")
	if err != nil {
		t.Fatalf("SecretBackendConfig (orig): %v", err)
	}
	origDefault, err := adm.secrets.DefaultBackend(ctx)
	if err != nil {
		t.Fatalf("DefaultBackend (orig): %v", err)
	}
	defer func() {
		if string(origCfg) != "{}" {
			if err := adm.SetSecretBackendConfig(ctx, "vault", origCfg); err != nil {
				t.Errorf("restore vault config: %v", err)
			}
		} else if err := adm.DeleteSecretBackendConfig(ctx, "vault"); err != nil {
			t.Errorf("remove test vault config: %v", err)
		}
		if err := adm.SetDefaultSecretBackend(ctx, origDefault); err != nil {
			t.Errorf("restore default backend: %v", err)
		}
	}()

	if err := adm.SetSecretBackendConfig(ctx, "vault",
		[]byte(`{"address":"`+srv.URL+`","mount":"kv","token_ref":"`+ref+`_TOKEN"}`)); err != nil {
		t.Fatalf("SetSecretBackendConfig: %v", err)
	}
	if err := adm.SetSecret(ctx, ref+"_TOKEN", "vault-tok"); err != nil {
		t.Fatalf("SetSecret token: %v", err)
	}

	// Set the ref itself through built-in storage (default still "db"
	// at this point), then migrate it onto vault explicitly.
	if err := adm.SetSecret(ctx, ref, "sk-secret-value"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	if err := adm.MigrateSecret(ctx, ref, "vault"); err != nil {
		t.Fatalf("MigrateSecret: %v", err)
	}
	if wroteValue != "sk-secret-value" {
		t.Fatalf("vault received value %q, want sk-secret-value", wroteValue)
	}
	if configured, backend, err := adm.SecretStatus(ctx, ref); err != nil || !configured || backend != "vault" {
		t.Fatalf("SecretStatus after migrate: configured=%v backend=%q err=%v", configured, backend, err)
	}

	var before, after []byte
	if err := db.QueryRow(ctx, `SELECT before, after FROM admin_audit
		WHERE entity = 'secret' AND entity_id = $1 AND action = 'migrate'
		ORDER BY ts DESC LIMIT 1`, ref).Scan(&before, &after); err != nil {
		t.Fatalf("audit query: %v", err)
	}
	if strings.Contains(string(before), "sk-secret-value") || strings.Contains(string(after), "sk-secret-value") {
		t.Fatalf("audit row leaked the secret value: before=%s after=%s", before, after)
	}
	var afterBody map[string]string
	if err := json.Unmarshal(after, &afterBody); err != nil {
		t.Fatalf("audit after %s: %v", after, err)
	}
	if afterBody["backend"] != "vault" {
		t.Fatalf("audit after = %v, want backend=vault", afterBody)
	}

	// Idempotent: migrating again to the same backend is a no-op.
	if err := adm.MigrateSecret(ctx, ref, "vault"); err != nil {
		t.Fatalf("MigrateSecret (idempotent): %v", err)
	}

	// Unknown target is rejected.
	if err := adm.MigrateSecret(ctx, ref, "nope"); err == nil {
		t.Fatal("MigrateSecret with unknown backend accepted")
	}
}

// TestMigrateAllSecretsBulkPartialFailure drives the bulk endpoint
// against two refs: one already on the target backend (skipped), one
// db-backed that migrates cleanly, and asserts a failure on one ref
// (unconfigured asm) never aborts the rest of the batch.
func TestMigrateAllSecretsBulkPartialFailure(t *testing.T) {
	adm, _, _ := testAdmin(t)
	ctx := t.Context()
	refOK := adminMarker + "migrate-all-ok"
	refSkip := adminMarker + "migrate-all-skip"

	origVaultCfg, err := adm.SecretBackendConfig(ctx, "vault")
	if err != nil {
		t.Fatalf("SecretBackendConfig (orig): %v", err)
	}
	defer func() {
		if string(origVaultCfg) != "{}" {
			if err := adm.SetSecretBackendConfig(ctx, "vault", origVaultCfg); err != nil {
				t.Errorf("restore vault config: %v", err)
			}
		} else if err := adm.DeleteSecretBackendConfig(ctx, "vault"); err != nil {
			t.Errorf("remove test vault config: %v", err)
		}
	}()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/auth/token/lookup-self":
			w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "migrate-all-fail"):
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == http.MethodPost:
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	if err := adm.SetSecretBackendConfig(ctx, "vault",
		[]byte(`{"address":"`+srv.URL+`","mount":"kv","token_ref":"`+refOK+`_TOKEN"}`)); err != nil {
		t.Fatalf("SetSecretBackendConfig: %v", err)
	}
	if err := adm.SetSecret(ctx, refOK+"_TOKEN", "vault-tok"); err != nil {
		t.Fatalf("SetSecret token: %v", err)
	}

	// refOK: db-backed, migrates cleanly to vault.
	if err := adm.SetSecret(ctx, refOK, "sk-a"); err != nil {
		t.Fatalf("SetSecret refOK: %v", err)
	}
	// refSkip: migrate it onto vault up front so the bulk call sees it
	// already there and skips it.
	if err := adm.MigrateSecret(ctx, refSkip, "vault"); err == nil {
		t.Fatal("expected refSkip to not exist yet")
	}
	if err := adm.SetSecret(ctx, refSkip, "sk-b"); err != nil {
		t.Fatalf("SetSecret refSkip: %v", err)
	}
	if err := adm.MigrateSecret(ctx, refSkip, "vault"); err != nil {
		t.Fatalf("pre-migrate refSkip: %v", err)
	}

	// refFail: db-backed, but the fake vault 500s its write — the batch
	// must record the error and keep going.
	refFail := adminMarker + "migrate-all-fail"
	if err := adm.SetSecret(ctx, refFail, "sk-c"); err != nil {
		t.Fatalf("SetSecret refFail: %v", err)
	}

	// refBootstrap: the vault backend's own token_ref, db-backed. The
	// bulk migrate must skip it up front (it can never migrate; reporting
	// the refusal as a failure made the UI nag forever), never abort the
	// batch.
	refBootstrap := refOK + "_TOKEN"

	results, err := adm.MigrateAllSecrets(ctx, "vault")
	if err != nil {
		t.Fatalf("MigrateAllSecrets: %v", err)
	}
	byName := map[string]SecretMigrationResult{}
	for _, r := range results {
		byName[r.Name] = r
	}
	if !byName[refOK].Migrated {
		t.Fatalf("refOK result = %+v, want migrated", byName[refOK])
	}
	if !byName[refSkip].Skipped {
		t.Fatalf("refSkip result = %+v, want skipped (already on vault)", byName[refSkip])
	}
	bootstrapResult := byName[refBootstrap]
	if !bootstrapResult.Skipped || bootstrapResult.Migrated || bootstrapResult.Error != "" {
		t.Fatalf("refBootstrap result = %+v, want skipped only", bootstrapResult)
	}
	if byName[refFail].Error == "" || byName[refFail].Migrated || byName[refFail].Skipped {
		t.Fatalf("refFail result = %+v, want error only", byName[refFail])
	}

	// An unknown target backend fails validation before touching any ref.
	if _, err := adm.MigrateAllSecrets(ctx, "nope"); err == nil {
		t.Fatal("MigrateAllSecrets with unknown backend accepted")
	}
}

// TestCatalogPricesResolvesByProviderRow is CatalogPrices' end-to-end
// coverage of the DB half resolvePricedModel's unit tests fake out: a
// real provider row (options.litellm_provider="zai", the repro's
// z.ai-served provider) resolves the pair within zai's own candidates —
// never cloudflare's priced entry of the same model segment — and an
// unknown provider name reports a nil price rather than erroring.
func TestCatalogPricesResolvesByProviderRow(t *testing.T) {
	adm, _, _ := testAdmin(t)
	ctx := t.Context()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"zai/glm-4.7-flash": {"litellm_provider": "zai", "mode": "chat"},
			"cloudflare/@cf/zai-org/glm-4.7-flash": {"litellm_provider": "cloudflare", "mode": "chat",
				"input_cost_per_token": 0.00000006, "output_cost_per_token": 0.0000004}
		}`))
	}))
	defer srv.Close()
	adm.catalog = catalog.NewWithURL(slog.New(slog.NewTextHandler(io.Discard, nil)), srv.URL)
	if _, err := adm.CatalogRefresh(ctx); err != nil {
		t.Fatalf("CatalogRefresh: %v", err)
	}

	name := adminMarker + "zai"
	if _, err := adm.Create(ctx, Provider{
		Name: name, Kind: "api", Driver: "openaicompat",
		BaseURL: "https://api.z.ai/v1", CredentialRef: "SOME_ENV_NAME",
		Options: map[string]string{"litellm_provider": "zai"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	priced, err := adm.CatalogPrices(ctx, []ProviderModel{
		{Provider: name, Model: "glm-4.7-flash"},
		{Provider: adminMarker + "no-such-provider", Model: "glm-4.7-flash"},
	})
	if err != nil {
		t.Fatalf("CatalogPrices: %v", err)
	}
	if len(priced) != 2 {
		t.Fatalf("priced len = %d, want 2", len(priced))
	}
	if priced[0].Price != nil {
		t.Fatalf("zai's glm-4.7-flash priced = %+v, want nil (free on zai, never cloudflare's rate)", priced[0].Price)
	}
	if priced[1].Price != nil {
		t.Fatalf("unknown provider priced = %+v, want nil", priced[1].Price)
	}
}

// TestTestPersistsOpenAIResponsesOnDefiniteResult is the real incident
// this probe exists for (Z.ai's coding-plan endpoint 404s /responses
// while chatting fine over /chat/completions): Test's chat probe
// succeeds, the /responses probe returns a definite 404 → false, Test
// persists options.openai_responses=false onto the provider row and
// reloads the snapshot, and TestResult.OK stays true throughout — a
// provider without /responses is still a perfectly good chat provider.
func TestTestPersistsOpenAIResponsesOnDefiniteResult(t *testing.T) {
	adm, store, _ := testAdmin(t)
	ctx := t.Context()

	var responsesHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat/completions":
			oaiWriteSSE(w, `{"choices":[{"delta":{"content":"pong"}}]}`)
			oaiWriteSSE(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
			oaiWriteSSE(w, "[DONE]")
		case "/responses":
			responsesHit = true
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	name := adminMarker + "responses-probe"
	id, err := adm.Create(ctx, Provider{
		Name: name, Kind: "api", Driver: "openaicompat",
		BaseURL: srv.URL, CredentialRef: adminMarker + "responses-probe-key", DefaultModel: "m1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	res, err := adm.Test(ctx, id, "")
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !res.OK {
		t.Fatalf("Test.OK = false, want true (chat probe succeeded): %+v", res)
	}
	if !responsesHit {
		t.Fatal("responses probe never hit /responses")
	}
	if res.ResponsesOK == nil || *res.ResponsesOK {
		t.Fatalf("ResponsesOK = %v, want false (404)", res.ResponsesOK)
	}

	list, err := adm.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found bool
	for _, p := range list {
		if p.ID != id {
			continue
		}
		found = true
		if p.Options["openai_responses"] != "false" {
			t.Fatalf("persisted options.openai_responses = %q, want \"false\"", p.Options["openai_responses"])
		}
	}
	if !found {
		t.Fatal("provider not found after Test")
	}

	waitSnapshot(t, store, name)
	rows, _ := store.Snapshot().Providers()
	var row *router.ProviderRow
	for i := range rows {
		if rows[i].Name == name {
			row = &rows[i]
		}
	}
	if row == nil {
		t.Fatal("provider row missing from snapshot after reload")
	}
	if row.OpenAIResponses == nil || *row.OpenAIResponses {
		t.Fatalf("snapshot row OpenAIResponses = %v, want false", row.OpenAIResponses)
	}
}

// TestValidateNeverPersistsOpenAIResponses: Validate probes an unsaved
// config (the create-time preview) and must only report the result,
// never write it anywhere — there's no provider row yet to write onto.
func TestValidateNeverPersistsOpenAIResponses(t *testing.T) {
	adm, _, _ := testAdmin(t)
	ctx := t.Context()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat/completions":
			oaiWriteSSE(w, `{"choices":[{"delta":{"content":"pong"}}]}`)
			oaiWriteSSE(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
			oaiWriteSSE(w, "[DONE]")
		case "/responses":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ref := adminMarker + "validate-responses-key"
	if err := adm.SetSecret(ctx, ref, "sk-test"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	t.Cleanup(func() { _ = adm.DeleteSecret(context.Background(), ref) })

	res, err := adm.Validate(ctx, Provider{
		Name: adminMarker + "validate-unsaved", Kind: "api", Driver: "openaicompat",
		BaseURL: srv.URL, CredentialRef: ref,
	}, "m1")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.OK {
		t.Fatalf("Validate.OK = false, want true: %+v", res)
	}
	if res.ResponsesOK == nil || !*res.ResponsesOK {
		t.Fatalf("ResponsesOK = %v, want true (2xx)", res.ResponsesOK)
	}

	// Nothing was ever created, so there is nothing in the providers
	// table this could have written onto — confirm no stray row exists
	// under either name Validate touched.
	list, err := adm.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, p := range list {
		if strings.HasPrefix(p.Name, adminMarker+"validate") {
			t.Fatalf("Validate must never persist a provider row, found: %+v", p)
		}
	}
}

// oaiWriteSSE writes one OpenAI-compat chat/completions SSE chunk,
// mirroring the provider package's own oaiWrite test helper (unexported
// there, so duplicated here for this package's Stream-driving probes).
func oaiWriteSSE(w http.ResponseWriter, data string) {
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	w.(http.Flusher).Flush()
}
