package admin

import (
	"context"
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/catalog"
	"github.com/SumonMSelim/timothy/internal/gateway/router"
)

func f64(v float64) *float64 { return &v }

// fakeCatalog is a minimal in-memory catalogLookup for router tests: it
// returns its whole seeded pool regardless of q/litellmProviders, since
// these tests only care about catalog.Match's downstream lookup by id.
type fakeCatalog struct{ models []catalog.Model }

func (f fakeCatalog) SearchProviders(ctx context.Context, q string, litellmProviders []string, limit int) ([]catalog.Model, error) {
	return f.models, nil
}

func routesSnapshot(t *testing.T, strategy string) *router.Snapshot {
	t.Helper()
	provRows := []router.ProviderRow{
		{ID: "p1", Name: "pricey", Kind: "api", Driver: "openaicompat",
			BaseURL: "https://a.example/v1", DefaultModel: "big", CredentialRef: "K1", Enabled: true},
		{ID: "p2", Name: "cheap", Kind: "api", Driver: "openaicompat",
			BaseURL: "https://b.example/v1", DefaultModel: "small", CredentialRef: "K2", Enabled: true},
		{ID: "p3", Name: "off", Kind: "api", Driver: "openaicompat",
			BaseURL: "https://c.example/v1", DefaultModel: "m", CredentialRef: "K3", Enabled: false},
	}
	routeRows := []router.RouteRow{{Name: "r", Strategy: strategy, Enabled: true, Chain: []router.ChainEntry{
		{ProviderID: "p1", Model: "big"},
		{ProviderID: "p3", Model: "m"},
		{ProviderID: "p2", Model: "small"},
	}}}
	cat := fakeCatalog{models: []catalog.Model{
		{ID: "big", ModelKey: "big", Mode: "chat", InputPerMTok: f64(10), OutputPerMTok: f64(25)},
		{ID: "small", ModelKey: "small", Mode: "chat", InputPerMTok: f64(0.5), OutputPerMTok: f64(1)},
	}}
	snap, _ := router.BuildSnapshot(provRows, routeRows, func(string) string { return "sk" }, cat)
	return snap
}

func TestResolvedForRouteOrdered(t *testing.T) {
	t.Parallel()
	snap := routesSnapshot(t, "ordered")
	snap.SetStats(map[string]router.ModelStats{
		"pricey/big": {Uptime: 0.9, LatencyMS: 800, TokensPerS: 40},
	})

	resolved, serving := resolvedForRoute(snap, "r")
	if len(resolved) != 3 {
		t.Fatalf("resolved len = %d, want 3", len(resolved))
	}
	first := resolved[0]
	if first.ProviderName != "pricey" || first.Model != "big" || !first.Usable {
		t.Fatalf("first entry = %+v", first)
	}
	if first.Score != nil || first.NormPrice != nil {
		t.Fatalf("ordered route carries scores: %+v", first)
	}
	if first.Uptime == nil || *first.Uptime != 0.9 || *first.LatencyMS != 800 || *first.TokensPerS != 40 {
		t.Fatalf("raw stats not mapped: %+v", first)
	}
	if *first.OutputPerMTok != 25 {
		t.Fatalf("price not mapped: %+v", first)
	}
	if first.InputPerMTok == nil || *first.InputPerMTok != 10 {
		t.Fatalf("input price not mapped: %+v", first)
	}
	if resolved[1].Usable || resolved[1].SkipReason != "disabled" {
		t.Fatalf("disabled entry = %+v", resolved[1])
	}
	// No ledger data and no data sentinels leak as zeros.
	if third := resolved[2]; third.Uptime != nil || third.LatencyMS != nil || third.TokensPerS != nil {
		t.Fatalf("no-data entry carries values: %+v", third)
	}
	if serving == nil || serving.ProviderID != "p1" || serving.Model != "big" {
		t.Fatalf("serving = %+v, want p1/big", serving)
	}
}

func TestResolvedForRouteScored(t *testing.T) {
	t.Parallel()
	snap := routesSnapshot(t, "price")

	resolved, serving := resolvedForRoute(snap, "r")
	if resolved[0].ProviderName != "cheap" {
		t.Fatalf("price strategy did not sort cheap first: %+v", resolved)
	}
	first := resolved[0]
	if first.Score == nil || first.NormPrice == nil || *first.NormPrice != 1 {
		t.Fatalf("scored fields missing: %+v", first)
	}
	// No ledger data: latency/tps norms are nil, scored neutrally.
	if first.NormLatency != nil || first.NormTPS != nil {
		t.Fatalf("no-data norms leaked: %+v", first)
	}
	if serving == nil || serving.ProviderID != "p2" {
		t.Fatalf("serving = %+v, want cheap (p2)", serving)
	}
}

func TestResolvedForRouteSkipsAllUnusable(t *testing.T) {
	t.Parallel()
	// No credential resolves: every entry is unhealthy, nothing serves.
	provRows := []router.ProviderRow{
		{ID: "p1", Name: "dark", Kind: "api", Driver: "openaicompat",
			BaseURL: "https://a.example/v1", DefaultModel: "m", CredentialRef: "MISSING", Enabled: true},
	}
	routeRows := []router.RouteRow{{Name: "r", Enabled: true, Chain: []router.ChainEntry{
		{ProviderID: "p1", Model: "m"},
	}}}
	snap, _ := router.BuildSnapshot(provRows, routeRows, func(string) string { return "" }, nil)

	resolved, serving := resolvedForRoute(snap, "r")
	if len(resolved) != 1 || resolved[0].Usable {
		t.Fatalf("resolved = %+v", resolved)
	}
	if resolved[0].SkipReason != "unhealthy: credential MISSING unresolved" {
		t.Fatalf("skip reason = %q", resolved[0].SkipReason)
	}
	if serving != nil {
		t.Fatalf("serving = %+v, want nil", serving)
	}
}

// TestResolvedForRouteSurfacesProviderKind covers D-051's remaining
// admin surface: the routes response still carries a chain entry's
// provider kind (api vs cli) so the web editor can distinguish them,
// even though harness selection itself moved off the chain entirely —
// a kind='cli' row mixed into a chat route is simply unusable for chat
// (no chat driver is ever built for it), same as any other unhealthy
// entry.
func TestResolvedForRouteSurfacesProviderKind(t *testing.T) {
	t.Parallel()
	provRows := []router.ProviderRow{
		{ID: "p1", Name: "anthropic", Kind: "api", Driver: "anthropic",
			DefaultModel: "sonnet", CredentialRef: "A_KEY", Enabled: true},
		{ID: "p2", Name: "claude-sub", Kind: "cli", Driver: "claude-cli",
			CredentialRef: "subscription", Enabled: true, AnthropicBaseURL: "http://localhost:9999"},
	}
	routeRows := []router.RouteRow{{Name: "r", Enabled: true, Chain: []router.ChainEntry{
		{ProviderID: "p1", Model: "sonnet"},
		{ProviderID: "p2", Model: "claude-sonnet-4"},
	}}}
	snap, _ := router.BuildSnapshot(provRows, routeRows, func(string) string { return "sk" }, nil)

	resolved, serving := resolvedForRoute(snap, "r")
	if len(resolved) != 2 {
		t.Fatalf("resolved len = %d, want 2", len(resolved))
	}
	api, exec := resolved[0], resolved[1]
	if api.ProviderKind != "api" || !api.Usable {
		t.Fatalf("api entry = %+v", api)
	}
	if exec.ProviderKind != "cli" || exec.Usable {
		t.Fatalf("cli-kind entry usable for chat: %+v", exec)
	}
	if serving == nil || serving.ProviderID != "p1" {
		t.Fatalf("serving = %+v, want the chat-usable entry", serving)
	}
}

func TestResolvedForRouteUnknownRoute(t *testing.T) {
	t.Parallel()
	snap := routesSnapshot(t, "ordered")
	resolved, serving := resolvedForRoute(snap, "no-such-route")
	if resolved != nil || serving != nil {
		t.Fatalf("unknown route = %+v, %+v", resolved, serving)
	}
}
