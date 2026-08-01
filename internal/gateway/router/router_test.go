package router

import (
	"errors"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/provider"
)

func testSnapshot(t *testing.T, lookups map[string]string) *Snapshot {
	t.Helper()
	provRows := []ProviderRow{
		{
			ID: "p1", Name: "anthropic", Kind: "api", Driver: "anthropic",
			DefaultModel: "sonnet", CredentialRef: "A_KEY", Enabled: true,
			Models: []ModelInfo{
				{ID: "opus", Prices: &ModelPrices{InputPerMTok: 15, OutputPerMTok: 75}},
				{ID: "sonnet"},
			},
		},
		{
			ID: "p2", Name: "grok", Kind: "api", Driver: "openaicompat",
			BaseURL: "https://api.x.example/v1", DefaultModel: "grok-4",
			CredentialRef: "X_KEY", Enabled: true,
			Models: []ModelInfo{{ID: "grok-4"}},
		},
		{
			ID: "p3", Name: "disabled-one", Kind: "api", Driver: "anthropic",
			DefaultModel: "m", CredentialRef: "A_KEY", Enabled: false,
		},
	}
	routeRows := []RouteRow{
		{Name: "coding", Chain: []ChainEntry{
			{ProviderID: "p1", Model: "sonnet"},
			{ProviderID: "p2", Model: "grok-4"},
		}, Enabled: true},
		{Name: "mini", Chain: []ChainEntry{
			{ProviderID: "p3", Model: "m"},
		}, Enabled: true},
		{Name: "off", Chain: []ChainEntry{
			{ProviderID: "p1", Model: "sonnet"},
		}, Enabled: false},
		{Name: "ghost", Chain: []ChainEntry{
			{ProviderID: "nope", Model: "m"},
		}, Enabled: true},
		{Name: "embedding", Chain: []ChainEntry{
			{ProviderID: "p1", Model: "embed-a"}, // anthropic driver: no embeddings
			{ProviderID: "p2", Model: "embed-b"},
		}, Enabled: true},
	}
	snap, err := BuildSnapshot(provRows, routeRows, func(ref string) string { return lookups[ref] })
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	return snap
}

func allKeys() map[string]string {
	return map[string]string{"A_KEY": "sk-a", "X_KEY": "sk-x"}
}

func attemptNames(attempts []Attempt) string {
	parts := make([]string, len(attempts))
	for i, a := range attempts {
		parts[i] = a.ProviderName + "/" + a.Model
	}
	return strings.Join(parts, ",")
}

func TestResolveChainOrder(t *testing.T) {
	t.Parallel()
	snap := testSnapshot(t, allKeys())

	attempts, err := snap.Resolve("coding", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := attemptNames(attempts); got != "anthropic/sonnet,grok/grok-4" {
		t.Fatalf("attempts = %s", got)
	}
}

func TestResolveHintProviderNameFirst(t *testing.T) {
	t.Parallel()
	snap := testSnapshot(t, allKeys())

	attempts, err := snap.Resolve("coding", "grok", Sticky{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Hint first, then the chain (dedup keeps grok once).
	if got := attemptNames(attempts); got != "grok/grok-4,anthropic/sonnet" {
		t.Fatalf("attempts = %s", got)
	}
}

func TestResolveHintExactModel(t *testing.T) {
	t.Parallel()
	snap := testSnapshot(t, allKeys())

	attempts, err := snap.Resolve("coding", "opus", Sticky{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if attempts[0].ProviderName != "anthropic" || attempts[0].Model != "opus" {
		t.Fatalf("first attempt = %s/%s, want anthropic/opus", attempts[0].ProviderName, attempts[0].Model)
	}
}

func TestResolveHintMissFallsToChain(t *testing.T) {
	t.Parallel()
	snap := testSnapshot(t, allKeys())

	attempts, err := snap.Resolve("coding", "no-such-thing", Sticky{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := attemptNames(attempts); got != "anthropic/sonnet,grok/grok-4" {
		t.Fatalf("attempts = %s", got)
	}
}

func TestResolveSkipsUnhealthy(t *testing.T) {
	t.Parallel()
	snap := testSnapshot(t, map[string]string{"X_KEY": "sk-x"}) // A_KEY unresolved

	attempts, err := snap.Resolve("coding", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := attemptNames(attempts); got != "grok/grok-4" {
		t.Fatalf("attempts = %s, want grok only", got)
	}
}

func TestResolveExhaustionNamesEveryReason(t *testing.T) {
	t.Parallel()
	snap := testSnapshot(t, allKeys())

	tests := []struct {
		category string
		want     []string
	}{
		{"mini", []string{"disabled-one", "disabled"}},
		{"ghost", []string{"unknown provider id"}},
		{"off", []string{"no usable provider"}},
		{"unrouted", []string{"no usable provider"}},
	}
	for _, tt := range tests {
		_, err := snap.Resolve(tt.category, "", Sticky{})
		var nre *NoRouteError
		if !errors.As(err, &nre) {
			t.Fatalf("%s: err = %v, want NoRouteError", tt.category, err)
		}
		for _, want := range tt.want {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s: error %q missing %q", tt.category, err.Error(), want)
			}
		}
	}
}

func TestResolveSkipsCapabilityMismatch(t *testing.T) {
	t.Parallel()
	snap := testSnapshot(t, allKeys())

	attempts, err := snap.Resolve("embedding", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// anthropic driver declares no embeddings capability: only the
	// openaicompat provider survives.
	if got := attemptNames(attempts); got != "grok/embed-b" {
		t.Fatalf("attempts = %s, want grok/embed-b only", got)
	}
}

func TestResolveCapabilityExhaustionNamesReason(t *testing.T) {
	t.Parallel()
	// Only the anthropic-driver provider is available for embedding.
	snap := testSnapshot(t, map[string]string{"A_KEY": "sk-a"})

	_, err := snap.Resolve("embedding", "", Sticky{})
	if err == nil || !strings.Contains(err.Error(), "lacks embeddings capability") {
		t.Fatalf("err = %v, want capability reason", err)
	}
}

func TestResolveModelLevelCapabilities(t *testing.T) {
	t.Parallel()
	// One provider whose driver can chat AND embed, with per-model
	// declarations that disagree: routing must judge each chain entry
	// by the model's own list, falling back to the driver only for
	// models that declare nothing.
	provRows := []ProviderRow{{
		ID: "p1", Name: "bedrock", Kind: "api", Driver: "openaicompat",
		BaseURL: "https://x.example/v1", DefaultModel: "nova",
		CredentialRef: "B_KEY", Enabled: true,
		Models: []ModelInfo{
			{ID: "nova", Capabilities: []string{"chat", "streaming", "tools"}},
			{ID: "titan-embed", Capabilities: []string{"embeddings"}},
			{ID: "undeclared"},
		},
	}}
	routeRows := []RouteRow{
		{Name: "coding", Chain: []ChainEntry{
			{ProviderID: "p1", Model: "titan-embed"}, // embeddings-only: skip
			{ProviderID: "p1", Model: "nova"},
			{ProviderID: "p1", Model: "undeclared"}, // driver decides: keep
		}, Enabled: true},
		{Name: "embedding", Chain: []ChainEntry{
			{ProviderID: "p1", Model: "nova"}, // chat-only model: skip
			{ProviderID: "p1", Model: "titan-embed"},
		}, Enabled: true},
	}
	snap, err := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	attempts, err := snap.Resolve("coding", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve coding: %v", err)
	}
	if got := attemptNames(attempts); got != "bedrock/nova,bedrock/undeclared" {
		t.Fatalf("coding attempts = %s", got)
	}

	attempts, err = snap.Resolve("embedding", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve embedding: %v", err)
	}
	if got := attemptNames(attempts); got != "bedrock/titan-embed" {
		t.Fatalf("embedding attempts = %s", got)
	}
}

func TestResolveModelCapabilityExhaustionNamesModel(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{{
		ID: "p1", Name: "bedrock", Kind: "api", Driver: "openaicompat",
		BaseURL: "https://x.example/v1", DefaultModel: "titan-embed",
		CredentialRef: "B_KEY", Enabled: true,
		Models: []ModelInfo{{ID: "titan-embed", Capabilities: []string{"embeddings"}}},
	}}
	routeRows := []RouteRow{{Name: "coding", Chain: []ChainEntry{
		{ProviderID: "p1", Model: "titan-embed"},
	}, Enabled: true}}
	snap, err := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	_, err = snap.Resolve("coding", "", Sticky{})
	if err == nil || !strings.Contains(err.Error(), "bedrock/titan-embed (lacks chat capability") {
		t.Fatalf("err = %v, want model-naming capability reason", err)
	}
}

// TestResolveVisionRejectsModelDeclaringOnlyChat confirms a model row
// that lists capabilities WITHOUT "vision" is skipped when the caller
// asks for CapVision as an extra requirement (D-045) — the model's own
// declaration wins over the driver's Capabilities() honesty.
func TestResolveVisionRejectsModelDeclaringOnlyChat(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{{
		ID: "p1", Name: "anthropic", Kind: "api", Driver: "anthropic",
		DefaultModel: "haiku", CredentialRef: "A_KEY", Enabled: true,
		Models: []ModelInfo{
			{ID: "haiku", Capabilities: []string{"chat", "streaming", "tools"}}, // no vision
			{ID: "sonnet"}, // undeclared: driver decides
		},
	}}
	routeRows := []RouteRow{{Name: "coding", Chain: []ChainEntry{
		{ProviderID: "p1", Model: "haiku"},
		{ProviderID: "p1", Model: "sonnet"},
	}, Enabled: true}}
	snap, err := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	attempts, err := snap.Resolve("coding", "", Sticky{}, provider.CapVision)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// haiku's explicit capability list omits vision: skipped. sonnet is
	// undeclared, so the anthropic driver's own CapVision decides: kept.
	if got := attemptNames(attempts); got != "anthropic/sonnet" {
		t.Fatalf("attempts = %s, want anthropic/sonnet only", got)
	}
}

// TestResolveVisionFallsToDriverWhenModelUndeclared confirms a model
// with NO capability list at all is judged by the driver alone, same
// rule attemptCapable already applies to every other capability.
func TestResolveVisionFallsToDriverWhenModelUndeclared(t *testing.T) {
	t.Parallel()
	snap := testSnapshot(t, allKeys()) // "sonnet" and "grok-4" both undeclared

	attempts, err := snap.Resolve("coding", "", Sticky{}, provider.CapVision)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Both the anthropic and openaicompat drivers declare CapVision;
	// neither model row restricts it, so both survive same as Resolve
	// without the extra requirement.
	if got := attemptNames(attempts); got != "anthropic/sonnet,grok/grok-4" {
		t.Fatalf("attempts = %s", got)
	}
}

// TestResolveVisionExhaustionMentionsVision confirms a chain with no
// vision-capable entry surfaces NoRouteError text naming "vision" —
// the sensitive-pinned-session-to-local-non-vision-model case reads
// clearly rather than a generic "no usable provider".
func TestResolveVisionExhaustionMentionsVision(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{{
		ID: "p1", Name: "local", Kind: "api", Driver: "anthropic",
		DefaultModel: "haiku", CredentialRef: "A_KEY", Enabled: true,
		Models: []ModelInfo{{ID: "haiku", Capabilities: []string{"chat", "streaming"}}}, // no vision
	}}
	routeRows := []RouteRow{{Name: "coding", Chain: []ChainEntry{
		{ProviderID: "p1", Model: "haiku"},
	}, Enabled: true}}
	snap, err := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	_, err = snap.Resolve("coding", "", Sticky{}, provider.CapVision)
	if err == nil || !strings.Contains(err.Error(), "lacks vision capability") {
		t.Fatalf("err = %v, want a reason mentioning vision", err)
	}
}

func TestProvidersSortedByName(t *testing.T) {
	t.Parallel()
	snap := testSnapshot(t, allKeys())

	rows, _ := snap.Providers()
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Name > rows[i].Name {
			t.Fatalf("providers not sorted: %s > %s", rows[i-1].Name, rows[i].Name)
		}
	}
}

func TestPrices(t *testing.T) {
	t.Parallel()
	snap := testSnapshot(t, allKeys())

	if p := snap.Prices("anthropic", "opus"); p == nil || p.InputPerMTok != 15 {
		t.Fatalf("Prices(anthropic, opus) = %+v", p)
	}
	if p := snap.Prices("anthropic", "sonnet"); p != nil {
		t.Fatalf("Prices(anthropic, sonnet) = %+v, want nil (unpriced)", p)
	}
	if p := snap.Prices("nope", "m"); p != nil {
		t.Fatalf("Prices(nope) = %+v, want nil", p)
	}
}

func TestRoutesListing(t *testing.T) {
	t.Parallel()
	snap := testSnapshot(t, allKeys())

	routes := snap.Routes()
	coding := routes["coding"]
	if len(coding) != 2 || coding[0]["provider"] != "anthropic" || coding[1]["provider"] != "grok" {
		t.Fatalf("coding route = %+v", coding)
	}
	if _, ok := routes["off"]; ok {
		t.Fatal("disabled route listed")
	}
}

// TestBedrockUnresolvedCredentialRefFailsBuild covers D-048: AWS
// profile/SSO mode is gone, so a bedrock row whose credential_ref does
// not resolve in the secret store is a hard BuildSnapshot error, not a
// degraded-but-usable provider.
func TestBedrockUnresolvedCredentialRefFailsBuild(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{{
		ID: "p1", Name: "bedrock", Kind: "api", Driver: "bedrock",
		DefaultModel: "us.amazon.nova-pro-v1:0",
		CredentialRef: "missing-secret", Enabled: true,
		Models: []ModelInfo{{ID: "titan-embed", Capabilities: []string{"embeddings"}}},
	}}
	routeRows := []RouteRow{{Name: "embedding", Chain: []ChainEntry{
		{ProviderID: "p1", Model: "titan-embed"},
	}, Enabled: true}}
	_, err := BuildSnapshot(provRows, routeRows, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "bedrock requires static keys in the secret store") {
		t.Fatalf("BuildSnapshot err = %v, want a clear static-keys-required message", err)
	}
}

// TestBedrockResolvingCredentialRefPassesStaticCredentialsThrough
// covers D-047 end to end at the router layer: when a bedrock row's
// credential_ref resolves in the secret store, the resolved JSON value
// must reach the bedrock constructor and populate StaticCredentials —
// the same lookup plumbing every other driver's APIKey field uses.
func TestBedrockResolvingCredentialRefPassesStaticCredentialsThrough(t *testing.T) {
	t.Parallel()
	secretJSON := `{"access_key_id":"AKIA123","secret_access_key":"shh"}` // #nosec G101
	provRows := []ProviderRow{{
		ID: "p1", Name: "bedrock", Kind: "api", Driver: "bedrock",
		DefaultModel: "us.amazon.nova-pro-v1:0",
		CredentialRef: "bedrock-static", Enabled: true,
		Models: []ModelInfo{{ID: "us.amazon.nova-pro-v1:0"}},
	}}
	routeRows := []RouteRow{{Name: "chat", Chain: []ChainEntry{
		{ProviderID: "p1", Model: "us.amazon.nova-pro-v1:0"},
	}, Enabled: true}}
	snap, err := BuildSnapshot(provRows, routeRows, func(ref string) string {
		if ref == "bedrock-static" {
			return secretJSON
		}
		return ""
	})
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	attempts, err := snap.Resolve("chat", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(attempts) != 1 || attempts[0].ProviderName != "bedrock" {
		t.Fatalf("attempts = %v, want bedrock", attempts)
	}
	b, ok := attempts[0].Provider.(*provider.Bedrock)
	if !ok {
		t.Fatalf("provider type = %T, want *provider.Bedrock", attempts[0].Provider)
	}
	if !b.HasStaticCredentials() {
		t.Fatal("resolved secret JSON must reach the bedrock constructor as StaticCredentials")
	}
}

// TestBedrockOptionsRegionThreadsToProvider covers D-048: a bedrock
// row's Region (as parsed from options.region by applyProviderOptions in
// store.go) reaches the built provider without failing resolution —
// registry_test.go's TestBuildBedrockOptionsRegion checks the resulting
// BedrockConfig.Region value directly at the provider.Build layer.
func TestBedrockOptionsRegionThreadsToProvider(t *testing.T) {
	t.Parallel()
	secretJSON := `{"access_key_id":"AKIA123","secret_access_key":"shh"}` // #nosec G101
	provRows := []ProviderRow{{
		ID: "p1", Name: "bedrock", Kind: "api", Driver: "bedrock",
		DefaultModel: "us.amazon.nova-pro-v1:0",
		CredentialRef: "bedrock-static", Enabled: true, Region: "ap-southeast-2",
		Models: []ModelInfo{{ID: "us.amazon.nova-pro-v1:0"}},
	}}
	routeRows := []RouteRow{{Name: "chat", Chain: []ChainEntry{
		{ProviderID: "p1", Model: "us.amazon.nova-pro-v1:0"},
	}, Enabled: true}}
	snap, err := BuildSnapshot(provRows, routeRows, func(ref string) string {
		if ref == "bedrock-static" {
			return secretJSON
		}
		return ""
	})
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	attempts, err := snap.Resolve("chat", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := attempts[0].Provider.(*provider.Bedrock); !ok {
		t.Fatalf("provider type = %T, want *provider.Bedrock", attempts[0].Provider)
	}
}

// A price-strategy route reorders its chain: the cheaper declared
// model jumps ahead of the written first entry, and dead entries sink.
func TestScoredStrategyReordersChain(t *testing.T) {
	provRows := []ProviderRow{
		{ID: "p1", Name: "pricey", Kind: "api", Driver: "openaicompat",
			BaseURL: "https://a.example/v1", DefaultModel: "big", CredentialRef: "K1", Enabled: true,
			Models: []ModelInfo{{ID: "big", Prices: &ModelPrices{OutputPerMTok: 25}}}},
		{ID: "p2", Name: "cheap", Kind: "api", Driver: "openaicompat",
			BaseURL: "https://b.example/v1", DefaultModel: "small", CredentialRef: "K2", Enabled: true,
			Models: []ModelInfo{{ID: "small", Prices: &ModelPrices{OutputPerMTok: 1}}}},
	}
	routeRows := []RouteRow{{Name: "r", Strategy: "price", Enabled: true, Chain: []ChainEntry{
		{ProviderID: "p1", Model: "big"},
		{ProviderID: "p2", Model: "small"},
	}}}
	snap, err := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	attempts, err := snap.Resolve("r", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if attempts[0].ProviderName != "cheap" {
		t.Fatalf("price strategy served %s first, want cheap", attempts[0].ProviderName)
	}

	// Mild degradation doesn't dethrone a 25x price advantage…
	snap.SetStats(map[string]ModelStats{
		"cheap/small": {Uptime: 0.8, LatencyMS: 900},
		"pricey/big":  {Uptime: 1.0, LatencyMS: 800},
	})
	attempts, err = snap.Resolve("r", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve with stats: %v", err)
	}
	if attempts[0].ProviderName != "cheap" {
		t.Fatalf("price strategy abandoned cheap on soft stats: %+v", attempts)
	}
	// …but uptime multiplies the score, so a provider failing almost
	// every request sinks under ANY strategy, price included.
	snap.SetStats(map[string]ModelStats{
		"cheap/small": {Uptime: 0.05, LatencyMS: 900},
		"pricey/big":  {Uptime: 1.0, LatencyMS: 800},
	})
	attempts, err = snap.Resolve("r", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve with hard-down stats: %v", err)
	}
	if attempts[0].ProviderName != "pricey" {
		t.Fatalf("a 5%%-uptime provider stayed first: %+v", attempts)
	}
}

// An ordered route ignores stats entirely — written order is the
// contract.
func TestOrderedStrategyIgnoresStats(t *testing.T) {
	snap := testSnapshot(t, map[string]string{"ANTH_KEY": "sk", "X_KEY": "sk", "A_KEY": "sk"})
	snap.SetStats(map[string]ModelStats{"anthropic/sonnet": {Uptime: 0.01}})
	attempts, err := snap.Resolve("coding", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if attempts[0].ProviderName != "anthropic" {
		t.Fatalf("ordered chain reordered by stats: %+v", attempts)
	}
}

func almostEqual(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}

// An ordered route's detail keeps written order, is unscored, and
// still carries the raw ledger stats for observability.
func TestResolveDetailOrdered(t *testing.T) {
	t.Parallel()
	snap := testSnapshot(t, allKeys())
	snap.SetStats(map[string]ModelStats{
		"anthropic/sonnet": {Uptime: 0.9, LatencyMS: 800, TokensPerS: 40},
	})

	detail := snap.ResolveDetail("coding")
	if len(detail) != 2 {
		t.Fatalf("detail len = %d, want 2", len(detail))
	}
	if detail[0].ProviderName != "anthropic" || detail[1].ProviderName != "grok" {
		t.Fatalf("order = %s,%s, want written order", detail[0].ProviderName, detail[1].ProviderName)
	}
	first := detail[0]
	if first.Scored || first.Score != 0 {
		t.Fatalf("ordered route scored: %+v", first)
	}
	if !first.Usable || first.SkipReason != "" {
		t.Fatalf("healthy entry not usable: %+v", first)
	}
	if first.Uptime != 0.9 || first.LatencyMS != 800 || first.TokensPerS != 40 {
		t.Fatalf("raw stats not carried: %+v", first)
	}
	if second := detail[1]; second.Uptime != -1 || second.LatencyMS != 0 {
		t.Fatalf("no-data sentinels wrong: %+v", second)
	}
}

// A scored route's detail matches Resolve's try order exactly and
// exposes the normalized factors behind it.
func TestResolveDetailScoredMatchesResolve(t *testing.T) {
	provRows := []ProviderRow{
		{ID: "p1", Name: "pricey", Kind: "api", Driver: "openaicompat",
			BaseURL: "https://a.example/v1", DefaultModel: "big", CredentialRef: "K1", Enabled: true,
			Models: []ModelInfo{{ID: "big", Prices: &ModelPrices{OutputPerMTok: 25}}}},
		{ID: "p2", Name: "cheap", Kind: "api", Driver: "openaicompat",
			BaseURL: "https://b.example/v1", DefaultModel: "small", CredentialRef: "K2", Enabled: true,
			Models: []ModelInfo{{ID: "small", Prices: &ModelPrices{OutputPerMTok: 1}}}},
	}
	routeRows := []RouteRow{{Name: "r", Strategy: "price", Enabled: true, Chain: []ChainEntry{
		{ProviderID: "p1", Model: "big"},
		{ProviderID: "p2", Model: "small"},
	}}}
	snap, err := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	detail := snap.ResolveDetail("r")
	attempts, err := snap.Resolve("r", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for i := range detail {
		if detail[i].ProviderName != attempts[i].ProviderName {
			t.Fatalf("detail order diverges from Resolve at %d: %s vs %s",
				i, detail[i].ProviderName, attempts[i].ProviderName)
		}
	}

	cheap, pricey := detail[0], detail[1]
	if cheap.ProviderName != "cheap" {
		t.Fatalf("cheapest not first: %+v", detail)
	}
	if !cheap.Scored || !almostEqual(cheap.NormPrice, 1) || !almostEqual(pricey.NormPrice, 1.0/25) {
		t.Fatalf("norm prices wrong: cheap=%+v pricey=%+v", cheap, pricey)
	}
	// price weights: 0.9 price + 0.02 latency + 0.02 tps, latency/tps
	// neutral (0.5) with no ledger data, no uptime multiplier.
	if !almostEqual(cheap.Score, 0.9+0.02) {
		t.Fatalf("cheap score = %v", cheap.Score)
	}
	if !almostEqual(pricey.Score, 0.9/25+0.02) {
		t.Fatalf("pricey score = %v", pricey.Score)
	}
}

// Missing factors stay sentinel -1 and score neutrally; a near-dead
// uptime is floored at 0.05 rather than zeroing the candidate.
func TestResolveDetailNeutralAndFloor(t *testing.T) {
	provRows := []ProviderRow{
		{ID: "p1", Name: "quiet", Kind: "api", Driver: "openaicompat",
			BaseURL: "https://a.example/v1", DefaultModel: "m1", CredentialRef: "K", Enabled: true,
			Models: []ModelInfo{{ID: "m1"}}},
		{ID: "p2", Name: "flaky", Kind: "api", Driver: "openaicompat",
			BaseURL: "https://b.example/v1", DefaultModel: "m2", CredentialRef: "K", Enabled: true,
			Models: []ModelInfo{{ID: "m2"}}},
	}
	routeRows := []RouteRow{{Name: "r", Strategy: "auto", Enabled: true, Chain: []ChainEntry{
		{ProviderID: "p1", Model: "m1"},
		{ProviderID: "p2", Model: "m2"},
	}}}
	snap, err := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}
	snap.SetStats(map[string]ModelStats{
		"flaky/m2": {Uptime: 0.01, LatencyMS: 100, TokensPerS: 10},
	})

	detail := snap.ResolveDetail("r")
	if detail[0].ProviderName != "quiet" {
		t.Fatalf("floored candidate outranked neutral one: %+v", detail)
	}
	quiet, flaky := detail[0], detail[1]
	if quiet.NormPrice != -1 || quiet.NormLatency != -1 || quiet.NormTPS != -1 || quiet.Uptime != -1 {
		t.Fatalf("no-data sentinels wrong: %+v", quiet)
	}
	// auto weights: all factors neutral, no uptime multiplier.
	if !almostEqual(quiet.Score, (0.6+0.1+0.05)*0.5) {
		t.Fatalf("quiet score = %v", quiet.Score)
	}
	// flaky is best (only) latency and tps candidate, price neutral,
	// then multiplied by the 0.05 uptime floor.
	if !almostEqual(flaky.Score, (0.6*0.5+0.1+0.05)*0.05) {
		t.Fatalf("flaky score = %v", flaky.Score)
	}
	if !almostEqual(flaky.NormLatency, 1) || !almostEqual(flaky.NormTPS, 1) {
		t.Fatalf("flaky norms wrong: %+v", flaky)
	}
}

// Unusable entries stay in the detail with the gate's reason: the UI
// shows why an entry is skipped instead of hiding it.
func TestResolveDetailUsability(t *testing.T) {
	t.Parallel()
	snap := testSnapshot(t, allKeys())

	if d := snap.ResolveDetail("mini"); len(d) != 1 || d[0].Usable || d[0].SkipReason != "disabled" {
		t.Fatalf("disabled provider detail = %+v", d)
	}
	if d := snap.ResolveDetail("ghost"); len(d) != 1 || d[0].Usable || d[0].SkipReason != "unknown provider id" || d[0].ProviderName != "" {
		t.Fatalf("unknown provider detail = %+v", d)
	}
	if d := snap.ResolveDetail("embedding"); len(d) != 2 || d[0].Usable || d[0].SkipReason != "lacks embeddings capability" {
		t.Fatalf("capability detail = %+v", d)
	}
	if d := snap.ResolveDetail("no-such-route"); len(d) != 0 {
		t.Fatalf("unknown route detail = %+v", d)
	}

	unhealthy := testSnapshot(t, map[string]string{"X_KEY": "sk-x"}) // A_KEY unresolved
	d := unhealthy.ResolveDetail("coding")
	if d[0].Usable || d[0].SkipReason != "unhealthy: credential A_KEY unresolved" {
		t.Fatalf("unhealthy detail = %+v", d[0])
	}
	if !d[1].Usable {
		t.Fatalf("healthy entry marked unusable: %+v", d[1])
	}
}

// A single-entry scored chain keeps written order (nothing to sort)
// but still reports its score and factors.
func TestResolveDetailSingleEntryScored(t *testing.T) {
	provRows := []ProviderRow{
		{ID: "p1", Name: "solo", Kind: "api", Driver: "openaicompat",
			BaseURL: "https://a.example/v1", DefaultModel: "m", CredentialRef: "K", Enabled: true,
			Models: []ModelInfo{{ID: "m", Prices: &ModelPrices{OutputPerMTok: 2}}}},
	}
	routeRows := []RouteRow{{Name: "r", Strategy: "price", Enabled: true, Chain: []ChainEntry{
		{ProviderID: "p1", Model: "m"},
	}}}
	snap, err := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	d := snap.ResolveDetail("r")
	if len(d) != 1 || !d[0].Scored {
		t.Fatalf("detail = %+v", d)
	}
	// Sole candidate is its own best price; latency/tps neutral.
	if !almostEqual(d[0].Score, 0.9+0.02) || !almostEqual(d[0].NormPrice, 1) {
		t.Fatalf("solo score = %+v", d[0])
	}
}

// Sticky moves a chain member to the front, but never smuggles in a
// provider+model outside the chain.
func TestStickyPrefersChainMemberOnly(t *testing.T) {
	snap := testSnapshot(t, map[string]string{"ANTH_KEY": "sk", "X_KEY": "sk", "A_KEY": "sk"})

	attempts, err := snap.Resolve("coding", "", Sticky{ProviderName: "grok", Model: "grok-4"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if attempts[0].ProviderName != "grok" {
		t.Fatalf("sticky chain member not first: %+v", attempts)
	}

	attempts, err = snap.Resolve("coding", "", Sticky{ProviderName: "grok", Model: "not-in-chain"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if attempts[0].ProviderName != "anthropic" {
		t.Fatalf("sticky outside the chain changed order: %+v", attempts)
	}
}
