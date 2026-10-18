package router

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/gateway/catalog"
	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

const openRouterURL = "https://openrouter.ai/api/v1"

// openRouterListing is a parsed openrouter.ai listing: paid, free, a
// model with only an input price and one with no price.
func openRouterListing() []provider.AvailableModel {
	return []provider.AvailableModel{
		{ID: "openai/gpt-4o", InputPerMTok: fp(2.5), OutputPerMTok: fp(10), CacheReadPerMTok: fp(1.25)},
		{ID: "meta-llama/llama-3.3-70b-instruct:free", InputPerMTok: fp(0), OutputPerMTok: fp(0)},
		{ID: "half/priced", InputPerMTok: fp(1)},
		{ID: "openrouter/auto"},
		{ID: "op/override", InputPerMTok: fp(9), OutputPerMTok: fp(9)},
		{ID: "catalog/model", InputPerMTok: fp(9), OutputPerMTok: fp(9)},
	}
}

func TestPricesLiveListing(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{
		{ID: "p1", Name: "openrouter", Kind: "api", Driver: "openaicompat", BaseURL: openRouterURL, Enabled: true,
			PricesByModel: map[string]ModelPrices{"op/override": {InputPerMTok: 1, OutputPerMTok: 2}}},
		{ID: "p2", Name: "kilo", Kind: "api", Driver: "openaicompat", BaseURL: "https://kilocode.example/api/openrouter", Enabled: true},
		{ID: "p3", Name: "kilo-off", Kind: "api", Driver: "openaicompat", BaseURL: "https://kilocode.example/api/openrouter", Enabled: true,
			ListingPricesOff: true},
		{ID: "p4", Name: "ant", Kind: "api", Driver: "anthropic", Enabled: true},
	}
	cat := &fakeCatalog{pool: []catalog.Model{
		catModel("catalog/model", "chat", fp(3)),
		catModel("catalog/only", "chat", fp(4)),
		catModel("op/override", "chat", fp(5)),
	}}
	snap, _ := BuildSnapshot(provRows, nil, func(string) string { return "" }, cat)
	snap.live = newLivePrices()
	for _, row := range provRows {
		snap.live.set(row.Name, openRouterListing())
	}

	tests := []struct {
		name     string
		provider string
		model    string
		want     *ModelPrices
	}{
		{name: "paid model priced from the listing", provider: "openrouter", model: "openai/gpt-4o",
			want: &ModelPrices{InputPerMTok: 2.5, OutputPerMTok: 10, CacheReadPerMTok: 1.25}},
		{name: "free model costs zero", provider: "openrouter", model: "meta-llama/llama-3.3-70b-instruct:free",
			want: &ModelPrices{}},
		{name: "prices_by_model wins", provider: "openrouter", model: "op/override",
			want: &ModelPrices{InputPerMTok: 1, OutputPerMTok: 2}},
		{name: "listing wins over the catalog", provider: "openrouter", model: "catalog/model",
			want: &ModelPrices{InputPerMTok: 9, OutputPerMTok: 9}},
		{name: "catalog used when the listing states nothing", provider: "openrouter", model: "catalog/only",
			want: &ModelPrices{InputPerMTok: 4}},
		{name: "listing_prices off falls back to the catalog", provider: "kilo-off", model: "catalog/model",
			want: &ModelPrices{InputPerMTok: 3}},
		{name: "half priced model stays unknown", provider: "openrouter", model: "half/priced"},
		{name: "unpriced model stays unknown", provider: "openrouter", model: "openrouter/auto"},
		{name: "unlisted model stays unknown", provider: "openrouter", model: "nope/model"},
		{name: "any openaicompat host reads its listing", provider: "kilo", model: "openai/gpt-4o",
			want: &ModelPrices{InputPerMTok: 2.5, OutputPerMTok: 10, CacheReadPerMTok: 1.25}},
		{name: "listing_prices off ignores the table", provider: "kilo-off", model: "openai/gpt-4o"},
		{name: "other drivers never read a live table", provider: "ant", model: "openai/gpt-4o"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := snap.Prices(tc.provider, tc.model)
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("Prices(%s, %s) = %+v, want %+v", tc.provider, tc.model, got, tc.want)
			}
		})
	}
}

func TestPricesWithoutLiveTable(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{{ID: "p1", Name: "openrouter", Kind: "api", Driver: "openaicompat", BaseURL: openRouterURL, Enabled: true}}
	snap, _ := BuildSnapshot(provRows, nil, func(string) string { return "" }, nil)
	if p := snap.Prices("openrouter", "openai/gpt-4o"); p != nil {
		t.Fatalf("Prices = %+v, want nil without a live table", p)
	}
}

// listerProvider is a chat provider whose listing is scripted.
type listerProvider struct {
	capsOnlyProvider
	calls  *int
	models []provider.AvailableModel
	err    error
}

func (p listerProvider) ListModels(context.Context) ([]provider.AvailableModel, error) {
	*p.calls++
	return p.models, p.err
}

func TestRefreshLivePrices(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{
		{ID: "p1", Name: "openrouter", Kind: "api", Driver: "openaicompat", BaseURL: openRouterURL, CredentialRef: "K", Enabled: true},
		{ID: "p2", Name: "openrouter-off", Kind: "api", Driver: "openaicompat", BaseURL: openRouterURL, CredentialRef: "K", Enabled: false},
		{ID: "p3", Name: "openrouter-nokey", Kind: "api", Driver: "openaicompat", BaseURL: openRouterURL, CredentialRef: "MISSING", Enabled: true},
		{ID: "p4", Name: "kilo", Kind: "api", Driver: "openaicompat", BaseURL: "https://kilocode.example/api/openrouter", CredentialRef: "K", Enabled: true},
		{ID: "p5", Name: "kilo-optout", Kind: "api", Driver: "openaicompat", BaseURL: "https://kilocode.example/api/openrouter", CredentialRef: "K", Enabled: true,
			ListingPricesOff: true},
		{ID: "p6", Name: "ant", Kind: "api", Driver: "anthropic", CredentialRef: "K", Enabled: true},
	}
	snap, _ := BuildSnapshot(provRows, nil, func(ref string) string {
		if ref == "K" {
			return "key"
		}
		return ""
	}, nil)
	calls := map[string]*int{}
	for _, row := range provRows {
		calls[row.Name] = new(int)
		snap.registry.Add(row.Name, listerProvider{
			capsOnlyProvider: capsOnlyProvider{name: row.Name, caps: []provider.Capability{provider.CapChat}},
			calls:            calls[row.Name], models: openRouterListing(),
		})
	}
	s := &Store{live: newLivePrices(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	snap.live = s.live

	t0 := time.Now()
	s.refreshLivePrices(t.Context(), snap, t0)
	for name, want := range map[string]int{"openrouter": 1, "openrouter-off": 0, "openrouter-nokey": 0, "kilo": 1, "kilo-optout": 0, "ant": 0} {
		if *calls[name] != want {
			t.Fatalf("%s listed %d times, want %d", name, *calls[name], want)
		}
	}
	if p := snap.Prices("openrouter", "openai/gpt-4o"); p == nil || p.InputPerMTok != 2.5 {
		t.Fatalf("Prices after refresh = %+v, want the listed price", p)
	}

	s.refreshLivePrices(t.Context(), snap, t0.Add(time.Minute))
	if *calls["openrouter"] != 1 {
		t.Fatalf("relisted within the interval: %d calls", *calls["openrouter"])
	}

	snap.registry.Add("openrouter", listerProvider{
		capsOnlyProvider: capsOnlyProvider{name: "openrouter", caps: []provider.Capability{provider.CapChat}},
		calls:            calls["openrouter"], err: errors.New("http 503"),
	})
	s.refreshLivePrices(t.Context(), snap, t0.Add(livePriceInterval))
	if *calls["openrouter"] != 2 {
		t.Fatalf("not relisted after the interval: %d calls", *calls["openrouter"])
	}
	if p := snap.Prices("openrouter", "openai/gpt-4o"); p == nil || p.InputPerMTok != 2.5 {
		t.Fatalf("Prices after a failed relist = %+v, want the last good price kept", p)
	}
}
