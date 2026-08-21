package admin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/catalog"
	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
	"github.com/SumonMSelim/timothy/internal/secretstore"
)

// testCatalogAdmin builds a real *Admin backed by a permanently
// degraded pool (empty DSN, per pgpool.New): fine for the catalog
// endpoints under test here, since CatalogRefresh/Status/Search never
// touch a.db — only provider CRUD does.
func testCatalogAdmin(t *testing.T, catalogURL string) *Admin {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := pgpool.New(t.Context(), "", log)
	masterKey := make([]byte, 32)
	secrets, err := secretstore.New(pool, masterKey)
	if err != nil {
		t.Fatalf("secretstore.New: %v", err)
	}
	cat := catalog.NewWithURL(log, catalogURL)
	return New(pool, nil, nil, nil, secrets, cat, log)
}

func TestCatalogRefreshAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"anthropic/claude-a": {"litellm_provider": "anthropic", "mode": "chat"}}`))
	}))
	defer srv.Close()

	adm := testCatalogAdmin(t, srv.URL)
	ctx := context.Background()

	st, err := adm.CatalogRefresh(ctx)
	if err != nil {
		t.Fatalf("CatalogRefresh: %v", err)
	}
	if st.EntryCount != 1 {
		t.Fatalf("EntryCount = %d, want 1", st.EntryCount)
	}

	status, err := adm.CatalogStatus(ctx)
	if err != nil {
		t.Fatalf("CatalogStatus: %v", err)
	}
	if status.EntryCount != 1 || status.FetchedAt == nil {
		t.Fatalf("status = %+v, want a fetched entry", status)
	}
}

func TestCatalogSearchThroughAdmin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"anthropic/claude-3-5-sonnet": {"litellm_provider": "anthropic", "mode": "chat"},
			"openai/gpt-4o": {"litellm_provider": "openai", "mode": "chat"}
		}`))
	}))
	defer srv.Close()

	adm := testCatalogAdmin(t, srv.URL)
	ctx := context.Background()
	if _, err := adm.CatalogRefresh(ctx); err != nil {
		t.Fatalf("CatalogRefresh: %v", err)
	}

	models, err := adm.CatalogSearch(ctx, "sonnet", "", 0)
	if err != nil {
		t.Fatalf("CatalogSearch: %v", err)
	}
	if len(models) != 1 || models[0].ModelKey != "anthropic/claude-3-5-sonnet" {
		t.Fatalf("models = %+v", models)
	}
}

// TestCatalogModelsWireShapeStripsOwnPrefix covers the id field
// CatalogSearch/CatalogModelsForProvider add to the wire shape: the
// entry's own litellm_provider prefix stripped from model_key (the id
// a provider's API actually accepts), with model_key kept alongside
// unchanged for reference. A bare key (openai/gpt-4o has no zai/xai
// style prefix relative to its own provider) passes through as-is.
func TestCatalogModelsWireShapeStripsOwnPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"zai/glm-4.5": {"litellm_provider": "zai", "mode": "chat"},
			"gpt-4o": {"litellm_provider": "openai", "mode": "chat"}
		}`))
	}))
	defer srv.Close()

	adm := testCatalogAdmin(t, srv.URL)
	ctx := context.Background()
	if _, err := adm.CatalogRefresh(ctx); err != nil {
		t.Fatalf("CatalogRefresh: %v", err)
	}

	models, err := adm.CatalogSearch(ctx, "", "", 0)
	if err != nil {
		t.Fatalf("CatalogSearch: %v", err)
	}
	byKey := make(map[string]catalog.Model, len(models))
	for _, m := range models {
		byKey[m.ModelKey] = m
	}
	if got := byKey["zai/glm-4.5"]; got.ID != "glm-4.5" {
		t.Fatalf("id = %q, want the stripped local id glm-4.5 (model_key %q kept)", got.ID, got.ModelKey)
	}
	if got := byKey["gpt-4o"]; got.ID != "gpt-4o" {
		t.Fatalf("id = %q, want the bare key unchanged", got.ID)
	}
}

// TestResolvePricedModel covers resolvePricedModel's provider-restricted
// matching without a DB: an unknown provider (nil row) always reports a
// nil price; the repro case (glm-4.7-flash served on zai, free) matches
// within zai's own candidates and never picks up cloudflare's priced
// entry of the same segment; a match with no price, and no match at
// all within the restricted pool, both report nil.
func TestResolvePricedModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"zai/glm-4.7-flash": {"litellm_provider": "zai", "mode": "chat"},
			"cloudflare/@cf/zai-org/glm-4.7-flash": {"litellm_provider": "cloudflare", "mode": "chat",
				"input_cost_per_token": 0.00000006, "output_cost_per_token": 0.0000004},
			"anthropic/claude-3-5-sonnet-20241022": {"litellm_provider": "anthropic", "mode": "chat",
				"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015},
			"openai/gpt-4o-unpriced": {"litellm_provider": "openai", "mode": "chat"}
		}`))
	}))
	defer srv.Close()

	adm := testCatalogAdmin(t, srv.URL)
	ctx := context.Background()
	if _, err := adm.CatalogRefresh(ctx); err != nil {
		t.Fatalf("CatalogRefresh: %v", err)
	}

	zaiRow := &Provider{Kind: "api", Driver: "openaicompat", Options: map[string]string{"litellm_provider": "zai"}}
	anthropicRow := &Provider{Kind: "api", Driver: "anthropic"}
	openaiRow := &Provider{Kind: "api", Driver: "openaicompat", Options: map[string]string{"litellm_provider": "openai"}}

	cases := []struct {
		name      string
		pair      ProviderModel
		row       *Provider
		wantPrice bool
		wantInput float64
	}{
		{"unknown provider reports nil regardless of a same-name match elsewhere",
			ProviderModel{Provider: "ghost", Model: "glm-4.7-flash"}, nil, false, 0},
		{"zai's free model never picks up cloudflare's priced entry of the same segment",
			ProviderModel{Provider: "zai", Model: "glm-4.7-flash"}, zaiRow, false, 0},
		{"segment match within the provider's own candidates",
			ProviderModel{Provider: "anthropic", Model: "claude-3-5-sonnet-20241022"}, anthropicRow, true, 3},
		{"matched but unpriced reports nil",
			ProviderModel{Provider: "openai", Model: "gpt-4o-unpriced"}, openaiRow, false, 0},
		{"no match at all within the restricted pool",
			ProviderModel{Provider: "openai", Model: "unknown-model"}, openaiRow, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolvePricedModel(ctx, adm.catalog, tc.pair, tc.row)
			if err != nil {
				t.Fatalf("resolvePricedModel: %v", err)
			}
			if got.Provider != tc.pair.Provider || got.Model != tc.pair.Model {
				t.Fatalf("got = %+v, want provider/model echoed back from the request", got)
			}
			if tc.wantPrice {
				if got.Price == nil || got.Price.InputPerMTok == nil || *got.Price.InputPerMTok != tc.wantInput {
					t.Fatalf("Price = %+v, want input_per_mtok %v", got.Price, tc.wantInput)
				}
			} else if got.Price != nil {
				t.Fatalf("Price = %+v, want nil", got.Price)
			}
		})
	}
}
