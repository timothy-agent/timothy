package admin

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

// modelListCatalog is the fixture LiteLLM source for the list builder
// tests: chat and non-chat rows across openai, anthropic and bedrock.
const modelListCatalog = `{
	"gpt-4o": {"litellm_provider": "openai", "mode": "chat", "input_cost_per_token": 0.000002, "output_cost_per_token": 0.000008, "max_input_tokens": 128000},
	"gpt-image-1": {"litellm_provider": "openai", "mode": "image_generation", "input_cost_per_token": 0.000005},
	"gpt-realtime": {"litellm_provider": "openai", "mode": "realtime"},
	"gpt-modeless": {"litellm_provider": "openai", "input_cost_per_token": 0.000001},
	"text-embedding-3-small": {"litellm_provider": "openai", "mode": "embedding"},
	"claude-sonnet-4-5": {"litellm_provider": "anthropic", "mode": "chat", "input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015, "max_input_tokens": 200000},
	"bedrock/anthropic.claude-x": {"litellm_provider": "bedrock", "mode": "chat", "input_cost_per_token": 0.000003, "output_cost_per_token": 0.000015},
	"anthropic.claude-x": {"litellm_provider": "bedrock_converse", "mode": "chat"},
	"bedrock/amazon.titan-embed": {"litellm_provider": "bedrock", "mode": "embedding"},
	"bedrock/amazon.nova-pro": {"litellm_provider": "bedrock", "mode": "chat", "max_input_tokens": 300000}
}`

func modelListAdmin(t *testing.T) *Admin {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(modelListCatalog))
	}))
	t.Cleanup(srv.Close)
	adm := testCatalogAdmin(t, srv.URL)
	if _, err := adm.CatalogRefresh(t.Context()); err != nil {
		t.Fatalf("CatalogRefresh: %v", err)
	}
	return adm
}

func staticFetch(ids ...string) modelFetch {
	return func(context.Context) ([]provider.AvailableModel, error) {
		out := make([]provider.AvailableModel, len(ids))
		for i, id := range ids {
			out[i] = provider.AvailableModel{ID: id}
		}
		return out, nil
	}
}

func modelsFetch(models ...provider.AvailableModel) modelFetch {
	return func(context.Context) ([]provider.AvailableModel, error) {
		return models, nil
	}
}

func ptrF(v float64) *float64 { return &v }
func ptrI(v int64) *int64     { return &v }

func sameF(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return math.Abs(*a-*b) < 1e-9
}

func sameI(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestModelList(t *testing.T) {
	openai := Provider{ID: "p-openai", Name: "OpenAI", Kind: "api", Driver: "openaicompat", BaseURL: "https://api.openai.com/v1"}
	tests := []struct {
		name  string
		p     Provider
		fetch modelFetch
		want  []ListedModel
	}{
		{
			name:  "live join carries catalog price and context",
			p:     openai,
			fetch: staticFetch("gpt-4o"),
			want: []ListedModel{{ID: "gpt-4o", Source: "live", PriceSource: "catalog",
				InputPerMTok: ptrF(2), OutputPerMTok: ptrF(8), MaxInputTokens: ptrI(128000)}},
		},
		{
			name:  "non chat-like match is dropped",
			p:     openai,
			fetch: staticFetch("gpt-image-1", "gpt-realtime", "text-embedding-3-small", "gpt-4o"),
			want: []ListedModel{{ID: "gpt-4o", Source: "live", PriceSource: "catalog",
				InputPerMTok: ptrF(2), OutputPerMTok: ptrF(8), MaxInputTokens: ptrI(128000)}},
		},
		{
			name:  "no match stays with null prices",
			p:     openai,
			fetch: staticFetch("ft:gpt-4o:acme", "gpt-4o"),
			want: []ListedModel{
				{ID: "ft:gpt-4o:acme", Source: "live"},
				{ID: "gpt-4o", Source: "live", PriceSource: "catalog", InputPerMTok: ptrF(2), OutputPerMTok: ptrF(8), MaxInputTokens: ptrI(128000)},
			},
		},
		{
			name:  "match without a mode stays",
			p:     openai,
			fetch: staticFetch("gpt-modeless"),
			want:  []ListedModel{{ID: "gpt-modeless", Source: "live", PriceSource: "catalog", InputPerMTok: ptrF(1)}},
		},
		{
			name: "listing price and context used when the catalog has no match",
			p:    Provider{ID: "p-or", Name: "OpenRouter", Kind: "api", Driver: "openaicompat", BaseURL: "https://openrouter.ai/api/v1"},
			fetch: modelsFetch(
				provider.AvailableModel{ID: "openai/gpt-4o", InputPerMTok: ptrF(2.5), OutputPerMTok: ptrF(10), ContextLength: ptrI(128000)},
				provider.AvailableModel{ID: "meta-llama/llama-3.3-70b-instruct:free", InputPerMTok: ptrF(0), OutputPerMTok: ptrF(0)},
				provider.AvailableModel{ID: "openrouter/auto"},
			),
			want: []ListedModel{
				{ID: "openai/gpt-4o", Source: "live", PriceSource: "listing", InputPerMTok: ptrF(2.5), OutputPerMTok: ptrF(10), MaxInputTokens: ptrI(128000)},
				{ID: "meta-llama/llama-3.3-70b-instruct:free", Source: "live", PriceSource: "listing", InputPerMTok: ptrF(0), OutputPerMTok: ptrF(0)},
				{ID: "openrouter/auto", Source: "live"},
			},
		},
		{
			name: "listing price wins over the catalog, catalog fills empty fields",
			p:    openai,
			fetch: modelsFetch(
				provider.AvailableModel{ID: "gpt-4o", InputPerMTok: ptrF(2.5), OutputPerMTok: ptrF(9)},
				provider.AvailableModel{ID: "gpt-modeless", OutputPerMTok: ptrF(7), ContextLength: ptrI(4096)},
			),
			want: []ListedModel{
				{ID: "gpt-4o", Source: "live", PriceSource: "listing", InputPerMTok: ptrF(2.5), OutputPerMTok: ptrF(9), MaxInputTokens: ptrI(128000)},
				{ID: "gpt-modeless", Source: "live", PriceSource: "catalog", InputPerMTok: ptrF(1), MaxInputTokens: ptrI(4096)},
			},
		},
		{
			name: "listing_prices off falls back to the catalog",
			p: Provider{ID: "p-openai-off", Name: "OpenAI", Kind: "api", Driver: "openaicompat", BaseURL: "https://api.openai.com/v1",
				Options: map[string]string{"listing_prices": "off"}},
			fetch: modelsFetch(
				provider.AvailableModel{ID: "gpt-4o", InputPerMTok: ptrF(2.5), OutputPerMTok: ptrF(9)},
				provider.AvailableModel{ID: "house-model", InputPerMTok: ptrF(1), OutputPerMTok: ptrF(1)},
			),
			want: []ListedModel{
				{ID: "gpt-4o", Source: "live", PriceSource: "catalog", InputPerMTok: ptrF(2), OutputPerMTok: ptrF(8), MaxInputTokens: ptrI(128000)},
				{ID: "house-model", Source: "live"},
			},
		},
		{
			name: "operator price wins over listing and catalog",
			p: Provider{ID: "p-openai-op", Name: "OpenAI", Kind: "api", Driver: "openaicompat", BaseURL: "https://api.openai.com/v1",
				Options: map[string]string{"prices_by_model": `{"gpt-4o":{"input_per_mtok":1,"output_per_mtok":3},"house-model":{"input_per_mtok":0,"output_per_mtok":0}}`}},
			fetch: modelsFetch(
				provider.AvailableModel{ID: "gpt-4o", InputPerMTok: ptrF(2.5), OutputPerMTok: ptrF(9)},
				provider.AvailableModel{ID: "house-model"},
			),
			want: []ListedModel{
				{ID: "gpt-4o", Source: "live", PriceSource: "operator", InputPerMTok: ptrF(1), OutputPerMTok: ptrF(3), MaxInputTokens: ptrI(128000)},
				{ID: "house-model", Source: "live", PriceSource: "operator", InputPerMTok: ptrF(0), OutputPerMTok: ptrF(0)},
			},
		},
		{
			name:  "unmapped host keeps every live id unpriced",
			p:     Provider{ID: "p-custom", Name: "mars-ai", Kind: "api", Driver: "openaicompat", BaseURL: "https://llm.example.com/v1"},
			fetch: staticFetch("gpt-4o", "house-model"),
			want:  []ListedModel{{ID: "gpt-4o", Source: "live"}, {ID: "house-model", Source: "live"}},
		},
		{
			name:  "cliproxy row keeps its own vendor only",
			p:     Provider{ID: "p-claude-sub", Name: "Claude Subscription", Kind: "api", Driver: "openaicompat", BaseURL: "http://cliproxy:8317/v1"},
			fetch: staticFetch("claude-sonnet-4-5", "gpt-4o", "gpt-image-1", "codex-auto-review"),
			want: []ListedModel{{ID: "claude-sonnet-4-5", Source: "live", PriceSource: "catalog",
				InputPerMTok: ptrF(3), OutputPerMTok: ptrF(15), MaxInputTokens: ptrI(200000)}},
		},
		{
			name:  "catalog fallback restricted to candidate vendors, chat only",
			p:     Provider{ID: "p-bedrock", Name: "Bedrock", Kind: "api", Driver: "bedrock"},
			fetch: nil,
			want: []ListedModel{
				{ID: "anthropic.claude-x", Source: "catalog"},
				{ID: "amazon.nova-pro", Source: "catalog", MaxInputTokens: ptrI(300000)},
			},
		},
		{
			name:  "catalog fallback with no candidate vendor is empty",
			p:     Provider{ID: "p-codex", Name: "Codex", Kind: "cli", Driver: "codex-cli"},
			fetch: nil,
			want:  []ListedModel{},
		},
		{
			name:  "claude-cli falls back to anthropic rows",
			p:     Provider{ID: "p-cc", Name: "Claude Code", Kind: "cli", Driver: "claude-cli"},
			fetch: nil,
			want: []ListedModel{{ID: "claude-sonnet-4-5", Source: "catalog", PriceSource: "catalog",
				InputPerMTok: ptrF(3), OutputPerMTok: ptrF(15), MaxInputTokens: ptrI(200000)}},
		},
		{
			name: "catalog fallback with an operator price",
			p: Provider{ID: "p-cc-op", Name: "Claude Code", Kind: "cli", Driver: "claude-cli",
				Options: map[string]string{"prices_by_model": `{"claude-sonnet-4-5":{"input_per_mtok":2,"output_per_mtok":10}}`}},
			fetch: nil,
			want: []ListedModel{{ID: "claude-sonnet-4-5", Source: "catalog", PriceSource: "operator",
				InputPerMTok: ptrF(2), OutputPerMTok: ptrF(10), MaxInputTokens: ptrI(200000)}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			adm := modelListAdmin(t)
			got, err := adm.modelList(t.Context(), tc.p, tc.fetch)
			if err != nil {
				t.Fatalf("modelList: %v", err)
			}
			if got == nil {
				t.Fatal("modelList returned nil, want a non-nil slice")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d rows %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i, w := range tc.want {
				g := got[i]
				if g.ID != w.ID || g.Source != w.Source || g.DisplayName != w.DisplayName || g.PriceSource != w.PriceSource ||
					!sameF(g.InputPerMTok, w.InputPerMTok) || !sameF(g.OutputPerMTok, w.OutputPerMTok) ||
					!sameI(g.MaxInputTokens, w.MaxInputTokens) {
					t.Fatalf("row %d = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}

func TestModelListCachesLiveListing(t *testing.T) {
	adm := modelListAdmin(t)
	p := Provider{ID: "p-cache", Name: "OpenAI", Kind: "api", Driver: "openaicompat", BaseURL: "https://api.openai.com/v1"}
	calls := 0
	fetch := func(context.Context) ([]provider.AvailableModel, error) {
		calls++
		return []provider.AvailableModel{{ID: "gpt-4o"}}, nil
	}
	for range 2 {
		got, err := adm.modelList(t.Context(), p, fetch)
		if err != nil {
			t.Fatalf("modelList: %v", err)
		}
		if len(got) != 1 || got[0].ID != "gpt-4o" || got[0].InputPerMTok == nil {
			t.Fatalf("got %+v", got)
		}
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1 (second call served from cache)", calls)
	}
}

func TestModelListPassesAuthErrorThroughUncached(t *testing.T) {
	adm := modelListAdmin(t)
	p := Provider{ID: "p-401", Name: "OpenAI", Kind: "api", Driver: "openaicompat", BaseURL: "https://api.openai.com/v1"}
	authErr := errors.New(`openaicompat: list models: http 401: {"error":{"message":"Incorrect API key provided"}}`)
	calls := 0
	fetch := func(context.Context) ([]provider.AvailableModel, error) {
		calls++
		return nil, authErr
	}
	for range 2 {
		_, err := adm.modelList(t.Context(), p, fetch)
		if !errors.Is(err, authErr) {
			t.Fatalf("err = %v, want the provider's own auth error", err)
		}
	}
	if calls != 2 {
		t.Fatalf("fetch calls = %d, want 2 (errors never cached)", calls)
	}
}

func TestModelsCacheExpires(t *testing.T) {
	c := newModelsCache()
	c.entries["p1"] = modelsCacheEntry{
		models:    []provider.AvailableModel{{ID: "stale"}},
		fetchedAt: time.Now().Add(-modelsCacheTTL - time.Second),
	}
	if _, ok := c.get("p1"); ok {
		t.Fatal("cache served an expired entry")
	}
	got, err := c.list(t.Context(), "p1", staticFetch("fresh"))
	if err != nil || len(got) != 1 || got[0].ID != "fresh" {
		t.Fatalf("list = %+v, %v, want a fresh fetch", got, err)
	}
	if got, ok := c.get("p1"); !ok || got[0].ID != "fresh" {
		t.Fatalf("get = %+v, %v, want the fresh entry cached", got, ok)
	}
}

func TestListedModelJSONShape(t *testing.T) {
	b, err := json.Marshal([]ListedModel{
		{ID: "house-model", Source: "live"},
		{ID: "gpt-4o", DisplayName: "GPT-4o", Source: "catalog", InputPerMTok: ptrF(2), OutputPerMTok: ptrF(8), MaxInputTokens: ptrI(128000), PriceSource: "catalog"},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `[{"id":"house-model","source":"live","input_per_mtok":null,"output_per_mtok":null,"max_input_tokens":null},` +
		`{"id":"gpt-4o","display_name":"GPT-4o","source":"catalog","input_per_mtok":2,"output_per_mtok":8,"max_input_tokens":128000,"price_source":"catalog"}]`
	if string(b) != want {
		t.Fatalf("json = %s\nwant   %s", b, want)
	}
}
