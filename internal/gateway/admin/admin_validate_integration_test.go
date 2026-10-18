//go:build integration

package admin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/SumonMSelim/timothy/internal/gateway/catalog"
)

// stubOpenAI serves the two endpoints the admin layer touches: a
// minimal chat/completions SSE stream and a /models listing.
func stubOpenAI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"pong"}}]}`)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"m-alpha"},{"id":"m-beta"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestValidateProbesWithoutPersisting(t *testing.T) {
	adm, _, pool := testAdmin(t)
	ctx := t.Context()
	srv := stubOpenAI(t)
	name := adminMarker + "validate"

	res, err := adm.Validate(ctx, Provider{
		Name: name, Kind: "api", Driver: "openaicompat", BaseURL: srv.URL,
	}, "m-alpha")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.OK || res.Model != "m-alpha" {
		t.Fatalf("Validate = %+v, want ok probe of m-alpha", res)
	}

	// Nothing persisted: validation must leave no provider row behind.
	db, _ := pool.Get()
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM providers WHERE name = $1`, name).Scan(&count); err != nil {
		t.Fatalf("providers count: %v", err)
	}
	if count != 0 {
		t.Fatalf("validate persisted %d provider row(s)", count)
	}

	// The probe books as purpose='test' like Test does.
	var purpose string
	if err := db.QueryRow(ctx, `SELECT COALESCE(purpose, '') FROM cost_ledger
		WHERE provider = $1 ORDER BY ts DESC LIMIT 1`, name).Scan(&purpose); err != nil {
		t.Fatalf("ledger row: %v", err)
	}
	if purpose != "test" {
		t.Fatalf("probe purpose = %q, want test", purpose)
	}

	// A dead endpoint fails honestly inside the result, not as an error.
	res, err = adm.Validate(ctx, Provider{
		Name: name, Kind: "api", Driver: "openaicompat", BaseURL: "http://127.0.0.1:1/v1",
	}, "m-alpha")
	if err != nil {
		t.Fatalf("Validate dead endpoint: %v", err)
	}
	if res.OK || res.Detail == "" {
		t.Fatalf("dead endpoint = %+v, want honest failure", res)
	}

	// Config errors reject before any probe.
	if _, err := adm.Validate(ctx, Provider{Name: name, Kind: "api", Driver: "nope"}, "m"); err == nil {
		t.Fatal("unknown driver accepted")
	}
	if _, err := adm.Validate(ctx, Provider{Name: name, Kind: "api", Driver: "openaicompat", BaseURL: srv.URL}, ""); err == nil {
		t.Fatal("missing model accepted")
	}
}

func TestConnectionProbePricesACostedModel(t *testing.T) {
	ctx := t.Context()
	srv := stubOpenAI(t)
	name := adminMarker + "priced-probe"

	// Test's cost probe prices via the router snapshot (Snapshot.Prices),
	// which reads router.Store's own catalog reference set at
	// construction — swapping admin.catalog after the fact (as
	// CatalogPrices-only tests do) never reaches it. Seed the shared
	// catalog before building admin/store. stubOpenAI's httptest server
	// binds 127.0.0.1, which candidatesForHost restricts to
	// litellm_provider "ollama" — match that so the seeded model is
	// actually a candidate.
	catSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"m-alpha": {"litellm_provider": "ollama", "mode": "chat",
			"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002}}`))
	}))
	defer catSrv.Close()
	cat := catalog.NewWithURL(slog.New(slog.NewTextHandler(io.Discard, nil)), catSrv.URL)
	if _, err := cat.Sync(ctx); err != nil {
		t.Fatalf("catalog sync: %v", err)
	}
	adm, _, pool := testAdminWithCatalog(t, cat)

	id, err := adm.Create(ctx, Provider{
		Name: name, Kind: "api", Driver: "openaicompat", BaseURL: srv.URL,
		DefaultModel: "m-alpha",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
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

	res, err := adm.Test(ctx, id, "m-alpha")
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !res.OK {
		t.Fatalf("Test = %+v, want ok", res)
	}

	// stubOpenAI reports 2 prompt + 1 completion token; at $1/$2 per
	// million that's (2*1 + 1*2) / 1e6.
	const want = (2*1.0 + 1*2.0) / 1_000_000.0
	db, _ := pool.Get()
	var got float64
	if err := db.QueryRow(ctx, `SELECT cost FROM cost_ledger
		WHERE provider = $1 ORDER BY ts DESC LIMIT 1`, name).Scan(&got); err != nil {
		t.Fatalf("ledger row: %v", err)
	}
	if got != want {
		t.Fatalf("cost = %v, want %v", got, want)
	}
}

func TestAvailableModelsLiveFirstWithCatalogPrices(t *testing.T) {
	ctx := t.Context()

	// httptest binds 127.0.0.1, which candidatesForHost maps to
	// litellm_provider "ollama": the fixture's ollama rows are the
	// openaicompat row's candidates, the bedrock rows the bedrock row's.
	catSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"m-alpha": {"litellm_provider": "ollama", "mode": "chat", "input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "max_input_tokens": 32000},
			"m-image": {"litellm_provider": "ollama", "mode": "image_generation", "input_cost_per_token": 0.000004},
			"bedrock/anthropic.claude-x": {"litellm_provider": "bedrock", "mode": "chat", "input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015},
			"bedrock/amazon.titan-embed": {"litellm_provider": "bedrock", "mode": "embedding"},
			"gpt-4o": {"litellm_provider": "openai", "mode": "chat"}
		}`))
	}))
	defer catSrv.Close()
	cat := catalog.NewWithURL(slog.New(slog.NewTextHandler(io.Discard, nil)), catSrv.URL)
	if _, err := cat.Sync(ctx); err != nil {
		t.Fatalf("catalog sync: %v", err)
	}
	adm, store, _ := testAdminWithCatalog(t, cat)

	var listCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		listCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m-alpha"},{"id":"m-image"},{"id":"m-house"}]}`))
	}))
	t.Cleanup(srv.Close)

	name := adminMarker + "models"
	id, err := adm.Create(ctx, Provider{
		Name: name, Kind: "api", Driver: "openaicompat",
		BaseURL: srv.URL, DefaultModel: "m-alpha",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	waitSnapshot(t, store, name)
	models, err := adm.AvailableModels(ctx, id)
	if err != nil {
		t.Fatalf("AvailableModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want m-alpha and m-house (m-image dropped)", models)
	}
	alpha, house := models[0], models[1]
	if alpha.ID != "m-alpha" || alpha.Source != "live" || !sameF(alpha.InputPerMTok, ptrF(1)) ||
		!sameF(alpha.OutputPerMTok, ptrF(2)) || !sameI(alpha.MaxInputTokens, ptrI(32000)) {
		t.Fatalf("m-alpha = %+v, want live with catalog price and context", alpha)
	}
	if house.ID != "m-house" || house.Source != "live" || house.InputPerMTok != nil || house.OutputPerMTok != nil || house.MaxInputTokens != nil {
		t.Fatalf("m-house = %+v, want live with null prices", house)
	}
	if _, err := adm.AvailableModels(ctx, id); err != nil {
		t.Fatalf("AvailableModels again: %v", err)
	}
	if n := listCalls.Load(); n != 1 {
		t.Fatalf("listing calls = %d, want 1 (second call cached)", n)
	}

	// A 401 from the provider is its own auth error, never a catalog list.
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided"}}`))
	}))
	t.Cleanup(authSrv.Close)
	authName := adminMarker + "models-401"
	aid, err := adm.Create(ctx, Provider{
		Name: authName, Kind: "api", Driver: "openaicompat",
		BaseURL: authSrv.URL, DefaultModel: "m-alpha",
	})
	if err != nil {
		t.Fatalf("Create 401 provider: %v", err)
	}
	waitSnapshot(t, store, authName)
	if _, err := adm.AvailableModels(ctx, aid); err == nil ||
		!strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Incorrect API key provided") {
		t.Fatalf("401 AvailableModels err = %v, want the provider's auth error", err)
	}

	// Bedrock cannot enumerate models: it lists its candidate vendors'
	// chat rows from the catalog. Static keys are the only supported
	// auth (D-048), so the credential_ref must resolve for the provider
	// to build and reach the serving snapshot at all.
	bedrockRef := adminMarker + "models-bedrock-key" //nolint:gosec // test fixture name, not a secret
	if err := adm.SetSecret(ctx, bedrockRef, `{"access_key_id":"AKIA123","secret_access_key":"shh"}`); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	bedrockName := adminMarker + "models-bedrock"
	bid, err := adm.Create(ctx, Provider{
		Name: bedrockName, Kind: "api", Driver: "bedrock",
		CredentialRef: bedrockRef, Options: map[string]string{"region": "us-east-1"},
	})
	if err != nil {
		t.Fatalf("Create bedrock: %v", err)
	}
	waitSnapshot(t, store, bedrockName)
	bedrockModels, err := adm.AvailableModels(ctx, bid)
	if err != nil {
		t.Fatalf("bedrock AvailableModels: %v", err)
	}
	if len(bedrockModels) != 1 || bedrockModels[0].ID != "anthropic.claude-x" || bedrockModels[0].Source != "catalog" ||
		!sameF(bedrockModels[0].InputPerMTok, ptrF(3)) {
		t.Fatalf("bedrock models = %+v, want the one bedrock chat row from the catalog", bedrockModels)
	}

	// kind='cli' rows never build a chat driver at all (D-051); only
	// cursor-cli has a models-listing endpoint of its own. claude-cli
	// (subscription auth, no such endpoint) falls back to the catalog
	// rather than failing on "not in the serving snapshot".
	claudeCliName := adminMarker + "models-claude-cli"
	ccid, err := adm.Create(ctx, Provider{
		Name: claudeCliName, Kind: "cli", Driver: "claude-cli", CredentialRef: "CC_TOKEN",
	})
	if err != nil {
		t.Fatalf("Create claude-cli: %v", err)
	}
	ccModels, err := adm.AvailableModels(ctx, ccid)
	if err != nil {
		t.Fatalf("claude-cli AvailableModels: %v", err)
	}
	if len(ccModels) != 0 {
		t.Fatalf("claude-cli models = %+v, want none (fixture has no anthropic rows)", ccModels)
	}

	// cursor-cli calls its own listing endpoint directly, bypassing the
	// serving snapshot entirely.
	cursorRef := adminMarker + "models-cursor-key" //nolint:gosec // test fixture name, not a secret
	if err := adm.SetSecret(ctx, cursorRef, "cursor-key"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	cursorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"composer-2.5","displayName":"Composer 2.5"}]}`))
	}))
	t.Cleanup(cursorSrv.Close)
	origURL := cursorModelsURL
	cursorModelsURL = cursorSrv.URL
	t.Cleanup(func() { cursorModelsURL = origURL })

	cursorName := adminMarker + "models-cursor"
	cid, err := adm.Create(ctx, Provider{
		Name: cursorName, Kind: "cli", Driver: "cursor-cli", CredentialRef: cursorRef,
	})
	if err != nil {
		t.Fatalf("Create cursor-cli: %v", err)
	}
	cursorModels, err := adm.AvailableModels(ctx, cid)
	if err != nil {
		t.Fatalf("cursor-cli AvailableModels: %v", err)
	}
	if len(cursorModels) != 1 || cursorModels[0].ID != "composer-2.5" || cursorModels[0].DisplayName != "Composer 2.5" ||
		cursorModels[0].Source != "live" || cursorModels[0].InputPerMTok != nil {
		t.Fatalf("cursor-cli models = %+v", cursorModels)
	}
}
