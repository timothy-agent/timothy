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
		{Name: "embedding", Capability: "embeddings", Chain: []ChainEntry{
			{ProviderID: "p1", Model: "embed-a"}, // anthropic driver: no embeddings
			{ProviderID: "p2", Model: "embed-b"},
		}, Enabled: true},
	}
	snap, _ := BuildSnapshot(provRows, routeRows, func(ref string) string { return lookups[ref] })
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
		{Name: "embedding", Capability: "embeddings", Chain: []ChainEntry{
			{ProviderID: "p1", Model: "nova"}, // chat-only model: skip
			{ProviderID: "p1", Model: "titan-embed"},
		}, Enabled: true},
	}
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })

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
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })

	_, err := snap.Resolve("coding", "", Sticky{})
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
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })

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
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })

	_, err := snap.Resolve("coding", "", Sticky{}, provider.CapVision)
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

// TestDisabledProviderSkippedByRouting covers the disabled-provider
// contract: a disabled row is still built into the registry (the admin
// connection probe reaches providers before they are enabled), stays
// unhealthy, keeps its place in rows/byName for admin visibility, and
// a chain entry pointing at it is skipped by routing.
func TestDisabledProviderSkippedByRouting(t *testing.T) {
	t.Parallel()
	snap := testSnapshot(t, allKeys())

	if _, ok := snap.Provider("disabled-one"); !ok {
		t.Fatal("disabled provider missing from the registry")
	}
	rows, healthy := snap.Providers()
	found := false
	for _, r := range rows {
		if r.Name == "disabled-one" {
			found = true
		}
	}
	if !found {
		t.Fatal("disabled provider missing from Providers() rows")
	}
	if healthy["disabled-one"] {
		t.Fatal("disabled provider reported healthy")
	}

	attempts, err := snap.Resolve("mini", "", Sticky{})
	var nre *NoRouteError
	if !errors.As(err, &nre) || attempts != nil {
		t.Fatalf("Resolve(mini) = %+v, %v, want NoRouteError skipping the disabled entry", attempts, err)
	}
	if !strings.Contains(err.Error(), "disabled-one (disabled") {
		t.Fatalf("err = %v, want a disabled skip reason", err)
	}
}

// TestBuildSnapshotOneInvalidProviderAmongTwo covers the second
// defect: a single misconfigured provider row (here, openaicompat
// missing its required base_url) must not take down routing for every
// other provider. The bad row surfaces as a BuildWarning and stays
// unhealthy; the good row keeps serving.
func TestBuildSnapshotOneInvalidProviderAmongTwo(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{
		{ID: "p1", Name: "bad", Kind: "api", Driver: "openaicompat", // no BaseURL: Build errors
			DefaultModel: "m", CredentialRef: "K", Enabled: true,
			Models: []ModelInfo{{ID: "m"}}},
		{ID: "p2", Name: "good", Kind: "api", Driver: "openaicompat",
			BaseURL: "https://good.example/v1", DefaultModel: "m", CredentialRef: "K", Enabled: true,
			Models: []ModelInfo{{ID: "m"}}},
	}
	routeRows := []RouteRow{{Name: "r", Enabled: true, Chain: []ChainEntry{
		{ProviderID: "p1", Model: "m"},
		{ProviderID: "p2", Model: "m"},
	}}}
	snap, warnings := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
	if len(warnings) != 1 || warnings[0].Provider != "bad" || !strings.Contains(warnings[0].Err.Error(), "requires base_url") {
		t.Fatalf("warnings = %+v, want one warning naming bad's base_url requirement", warnings)
	}
	if _, ok := snap.Provider("bad"); ok {
		t.Fatal("invalid provider built into the registry")
	}
	if _, ok := snap.Provider("good"); !ok {
		t.Fatal("valid provider missing from the registry")
	}

	attempts, err := snap.Resolve("r", "", Sticky{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := attemptNames(attempts); got != "good/m" {
		t.Fatalf("attempts = %s, want good/m only", got)
	}
}

// TestBuildSnapshotAllProvidersInvalidStillBuilds covers the same
// defect at its extreme: every provider row fails to build. The
// snapshot must still build (an empty registry is a valid degraded
// state), and Resolve falls through to the existing, well-handled
// no-usable-provider path rather than any build-time failure.
func TestBuildSnapshotAllProvidersInvalidStillBuilds(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{
		{ID: "p1", Name: "bad1", Kind: "api", Driver: "openaicompat", // no BaseURL
			DefaultModel: "m", CredentialRef: "K", Enabled: true, Models: []ModelInfo{{ID: "m"}}},
		{ID: "p2", Name: "bad2", Kind: "api", Driver: "openaicompat", // no BaseURL
			DefaultModel: "m", CredentialRef: "K", Enabled: true, Models: []ModelInfo{{ID: "m"}}},
	}
	routeRows := []RouteRow{{Name: "r", Enabled: true, Chain: []ChainEntry{
		{ProviderID: "p1", Model: "m"},
		{ProviderID: "p2", Model: "m"},
	}}}
	snap, warnings := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
	if len(warnings) != 2 {
		t.Fatalf("warnings = %+v, want two", warnings)
	}

	_, err := snap.Resolve("r", "", Sticky{})
	var nre *NoRouteError
	if !errors.As(err, &nre) {
		t.Fatalf("err = %v, want the existing NoRouteError path", err)
	}
}

// TestBedrockUnresolvedCredentialRefDegradesNotFails covers D-048: AWS
// profile/SSO mode is gone, so a bedrock row whose credential_ref does
// not resolve in the secret store fails to build its own driver — but
// per-provider degradation means BuildSnapshot itself never fails: the
// row surfaces as a warning and stays unhealthy rather than taking
// down the whole snapshot.
func TestBedrockUnresolvedCredentialRefDegradesNotFails(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{{
		ID: "p1", Name: "bedrock", Kind: "api", Driver: "bedrock",
		DefaultModel:  "us.amazon.nova-pro-v1:0",
		CredentialRef: "missing-secret", Enabled: true,
		Models: []ModelInfo{{ID: "titan-embed", Capabilities: []string{"embeddings"}}},
	}}
	routeRows := []RouteRow{{Name: "embedding", Chain: []ChainEntry{
		{ProviderID: "p1", Model: "titan-embed"},
	}, Enabled: true}}
	snap, warnings := BuildSnapshot(provRows, routeRows, func(string) string { return "" })
	if len(warnings) != 1 || warnings[0].Provider != "bedrock" ||
		!strings.Contains(warnings[0].Err.Error(), "bedrock requires static keys in the secret store") {
		t.Fatalf("warnings = %+v, want one bedrock static-keys-required warning", warnings)
	}
	if _, err := snap.Resolve("embedding", "", Sticky{}); err == nil || !strings.Contains(err.Error(), "unhealthy") {
		t.Fatalf("Resolve err = %v, want unhealthy skip", err)
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
		DefaultModel:  "us.amazon.nova-pro-v1:0",
		CredentialRef: "bedrock-static", Enabled: true,
		Models: []ModelInfo{{ID: "us.amazon.nova-pro-v1:0"}},
	}}
	routeRows := []RouteRow{{Name: "chat", Chain: []ChainEntry{
		{ProviderID: "p1", Model: "us.amazon.nova-pro-v1:0"},
	}, Enabled: true}}
	snap, _ := BuildSnapshot(provRows, routeRows, func(ref string) string {
		if ref == "bedrock-static" {
			return secretJSON
		}
		return ""
	})

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
		DefaultModel:  "us.amazon.nova-pro-v1:0",
		CredentialRef: "bedrock-static", Enabled: true, Region: "ap-southeast-2",
		Models: []ModelInfo{{ID: "us.amazon.nova-pro-v1:0"}},
	}}
	routeRows := []RouteRow{{Name: "chat", Chain: []ChainEntry{
		{ProviderID: "p1", Model: "us.amazon.nova-pro-v1:0"},
	}, Enabled: true}}
	snap, _ := BuildSnapshot(provRows, routeRows, func(ref string) string {
		if ref == "bedrock-static" {
			return secretJSON
		}
		return ""
	})

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
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })

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
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })

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
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
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
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })

	d := snap.ResolveDetail("r")
	if len(d) != 1 || !d[0].Scored {
		t.Fatalf("detail = %+v", d)
	}
	// Sole candidate is its own best price; latency/tps neutral.
	if !almostEqual(d[0].Score, 0.9+0.02) || !almostEqual(d[0].NormPrice, 1) {
		t.Fatalf("solo score = %+v", d[0])
	}
}

// A chain entry with no model of its own serves the provider's
// DefaultModel — prices and stats must be looked up under that
// resolved model, not the empty raw entry model, or a default-model
// entry always shows unpriced/no-data and scores neutrally regardless
// of real ledger history.
func TestResolveDetailEmptyModelUsesDefaultModelForPricesAndStats(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{
		{ID: "p1", Name: "defaulted", Kind: "api", Driver: "openaicompat",
			BaseURL: "https://a.example/v1", DefaultModel: "m1", CredentialRef: "K", Enabled: true,
			Models: []ModelInfo{{ID: "m1", Prices: &ModelPrices{OutputPerMTok: 5}}}},
	}
	routeRows := []RouteRow{{Name: "r", Strategy: "price", Enabled: true, Chain: []ChainEntry{
		{ProviderID: "p1", Model: ""},
	}}}
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
	snap.SetStats(map[string]ModelStats{
		"defaulted/m1": {Uptime: 0.9, LatencyMS: 500, TokensPerS: 20},
	})

	d := snap.ResolveDetail("r")
	if len(d) != 1 {
		t.Fatalf("detail = %+v", d)
	}
	if d[0].Model != "m1" {
		t.Fatalf("Model = %q, want defaulted m1", d[0].Model)
	}
	if d[0].OutputPerMTok != 5 {
		t.Fatalf("OutputPerMTok = %v, want 5 (from default model's price row)", d[0].OutputPerMTok)
	}
	if d[0].Uptime != 0.9 || d[0].LatencyMS != 500 || d[0].TokensPerS != 20 {
		t.Fatalf("stats not resolved via default model: %+v", d[0])
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

// harnessSnapshot builds a chat route whose chain is pure
// {provider_id, model} entries (D-051 rework — harness is no longer a
// chain field), plus a subscription-auth row with no chat driver at
// all — the shape a claude-cli executor entry actually takes when
// ResolveRoute is called with a harness.
func harnessSnapshot(t *testing.T) *Snapshot {
	t.Helper()
	provRows := []ProviderRow{
		{ID: "p1", Name: "anthropic", Kind: "api", Driver: "anthropic",
			DefaultModel: "sonnet", CredentialRef: "A_KEY", Enabled: true},
		{ID: "p2", Name: "claude-sub", Kind: "cli", Driver: "claude-cli",
			CredentialRef: "subscription", Enabled: true, AnthropicBaseURL: "http://localhost:9999"},
	}
	routeRows := []RouteRow{
		{Name: "coding", Enabled: true, Chain: []ChainEntry{
			{ProviderID: "p1", Model: "sonnet"},
			{ProviderID: "p2", Model: "claude-sonnet-4"},
		}},
	}
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
	return snap
}

func TestResolveRouteMixedChainNativeAxis(t *testing.T) {
	t.Parallel()
	snap := harnessSnapshot(t)

	// harness == "" evaluates every entry on the chat axis: the
	// subscription-auth row (kind='cli', no chat driver built) is
	// correctly unusable for chat, not because of any harness field but
	// because it's not in the chat registry at all.
	entries, ok := snap.ResolveRoute("coding", "")
	if !ok {
		t.Fatalf("ResolveRoute: route not found")
	}
	if len(entries) != 2 {
		t.Fatalf("entries len = %d, want 2", len(entries))
	}
	api := entries[0]
	if !api.Usable || api.SkipReason != "" {
		t.Fatalf("api entry = %+v", api)
	}
	if entries[1].Usable {
		t.Fatalf("cli-kind entry usable on the chat axis: %+v", entries[1])
	}
}

func TestResolveRouteMixedChainExecutorAxis(t *testing.T) {
	t.Parallel()
	snap := harnessSnapshot(t)

	// harness != "" evaluates every entry on the executor axis instead:
	// the plain api/anthropic row is now unusable (no credential
	// resolution issue — executorUsable just applies a different rule),
	// the subscription row is usable.
	entries, ok := snap.ResolveRoute("coding", "claude-cli")
	if !ok {
		t.Fatalf("ResolveRoute: route not found")
	}
	if len(entries) != 2 {
		t.Fatalf("entries len = %d, want 2", len(entries))
	}
	exec := entries[1]
	if !exec.Usable || exec.SkipReason != "" {
		t.Fatalf("executor entry = %+v, want usable claude-cli", exec)
	}
	if exec.CredentialRef != "subscription" {
		t.Fatalf("executor credential_ref = %q, want a name only", exec.CredentialRef)
	}
	// A kind='cli' row never gets the anthropic_base_url override
	// (bugfix): it spawns its own CLI against the vendor's default
	// endpoint under subscription/oauth credentials.
	if exec.BaseURL != "" {
		t.Fatalf("executor base_url = %q, want empty for a kind='cli' row", exec.BaseURL)
	}
}

// TestKindCliHealthyMeansCredentialResolves: a kind='cli' row has no
// chat driver to probe, so Providers()'s healthy map must judge it
// purely on whether its credential_ref resolves — same rule as an
// api-kind row, just without ever reaching provider.Build.
func TestKindCliHealthyMeansCredentialResolves(t *testing.T) {
	t.Parallel()
	snap := harnessSnapshot(t)

	_, healthy := snap.Providers()
	if !healthy["claude-sub"] {
		t.Fatal("kind='cli' row with a resolving credential_ref reported unhealthy")
	}

	provRows := []ProviderRow{
		{ID: "p2", Name: "claude-sub", Kind: "cli", Driver: "claude-cli",
			CredentialRef: "subscription", Enabled: true},
	}
	unresolved, _ := BuildSnapshot(provRows, nil, func(string) string { return "" })
	_, healthy = unresolved.Providers()
	if healthy["claude-sub"] {
		t.Fatal("kind='cli' row with an unresolved credential_ref reported healthy")
	}
}

func TestResolveRouteHarnessOnly(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{
		{ID: "p2", Name: "claude-sub", Kind: "cli", Driver: "claude-cli",
			CredentialRef: "subscription", Enabled: true, AnthropicBaseURL: "http://localhost:9999"},
	}
	routeRows := []RouteRow{
		{Name: "coding-exec", Enabled: true, Chain: []ChainEntry{
			{ProviderID: "p2", Model: "claude-sonnet-4"},
		}},
	}
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })

	entries, ok := snap.ResolveRoute("coding-exec", "claude-cli")
	if !ok || len(entries) != 1 {
		t.Fatalf("ResolveRoute = %+v, ok=%v", entries, ok)
	}
	if !entries[0].Usable {
		t.Fatalf("entry = %+v, want usable", entries[0])
	}
}

func TestResolveRouteUnknownHarnessParam(t *testing.T) {
	t.Parallel()
	snap := harnessSnapshot(t)
	// An unknown harness name in the query param itself is a hard
	// "route not found" — the caller (gateway API layer) turns this
	// into a 400, distinct from an existing route with an unusable entry.
	if _, ok := snap.ResolveRoute("coding", "nonexistent-cli"); ok {
		t.Fatalf("ResolveRoute: want ok=false for unknown harness param")
	}
}

func TestResolveRouteWireIncompatibleProvider(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{
		// kind='api', openaicompat driver, no anthropic_base_url set:
		// claude-cli cannot speak this provider's wire format.
		{ID: "p2", Name: "grok-sub", Kind: "api", Driver: "openaicompat",
			CredentialRef: "subscription", Enabled: true},
	}
	routeRows := []RouteRow{
		{Name: "coding-exec", Enabled: true, Chain: []ChainEntry{
			{ProviderID: "p2", Model: "m"},
		}},
	}
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })

	entries, ok := snap.ResolveRoute("coding-exec", "claude-cli")
	if !ok || len(entries) != 1 {
		t.Fatalf("ResolveRoute = %+v, ok=%v", entries, ok)
	}
	if entries[0].Usable || !strings.Contains(entries[0].SkipReason, "wire-incompatible") {
		t.Fatalf("entry = %+v, want wire-incompatible", entries[0])
	}
}

func TestResolveRouteCLIKindInherentlyWireCompatible(t *testing.T) {
	t.Parallel()
	// A kind='cli' row with no anthropic_base_url set must still resolve
	// usable on the executor axis: it spawns claude-cli against the
	// vendor's own default endpoint under subscription/oauth
	// credentials, never a third-party anthropic-compatible one.
	provRows := []ProviderRow{
		{ID: "p2", Name: "claude-sub", Kind: "cli", Driver: "claude-cli",
			CredentialRef: "subscription", Enabled: true},
	}
	routeRows := []RouteRow{
		{Name: "coding-exec", Enabled: true, Chain: []ChainEntry{
			{ProviderID: "p2", Model: "m"},
		}},
	}
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })

	entries, ok := snap.ResolveRoute("coding-exec", "claude-cli")
	if !ok || len(entries) != 1 {
		t.Fatalf("ResolveRoute = %+v, ok=%v", entries, ok)
	}
	if !entries[0].Usable || entries[0].SkipReason != "" {
		t.Fatalf("entry = %+v, want usable with no skip reason", entries[0])
	}
	if entries[0].BaseURL != "" {
		t.Fatalf("entry base_url = %q, want empty for a kind='cli' row", entries[0].BaseURL)
	}
}

// TestResolveRouteCLIKindServesOnlyItsOwnHarness: a kind='cli' row
// exists to serve the harness named by its own driver only. A pi
// mission resolving a chain that includes the claude-cli subscription
// row must see it as unusable (bugfix) — pi's BuildInvocation has no
// wire format for a kind='cli' row and rejects the spawn outright, so
// resolving it usable=true silently falls back to native.
func TestResolveRouteCLIKindServesOnlyItsOwnHarness(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{
		{ID: "p2", Name: "claude-sub", Kind: "cli", Driver: "claude-cli",
			CredentialRef: "subscription", Enabled: true},
	}
	routeRows := []RouteRow{
		{Name: "coding-exec", Enabled: true, Chain: []ChainEntry{
			{ProviderID: "p2", Model: "m"},
		}},
	}
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })

	entries, ok := snap.ResolveRoute("coding-exec", "pi")
	if !ok || len(entries) != 1 {
		t.Fatalf("ResolveRoute = %+v, ok=%v", entries, ok)
	}
	if entries[0].Usable || entries[0].SkipReason != "cli provider row serves the claude-cli harness" {
		t.Fatalf("entry = %+v, want unusable with the cli-harness-mismatch reason", entries[0])
	}

	// Regression: claude-cli against its own cli row stays usable.
	entries, ok = snap.ResolveRoute("coding-exec", "claude-cli")
	if !ok || len(entries) != 1 {
		t.Fatalf("ResolveRoute = %+v, ok=%v", entries, ok)
	}
	if !entries[0].Usable || entries[0].SkipReason != "" {
		t.Fatalf("entry = %+v, want usable with no skip reason", entries[0])
	}
}

func TestResolveRouteUnknownRoute(t *testing.T) {
	t.Parallel()
	snap := harnessSnapshot(t)
	if _, ok := snap.ResolveRoute("no-such-route", ""); ok {
		t.Fatalf("ResolveRoute: want ok=false for unknown route")
	}
}

// TestResolveRoutePiWireVariants covers pi's dual-wire support and the
// Wire field ResolveRoute now annotates every kind='api' executor entry
// with: an openaicompat row is directly usable for pi (unlike
// claude-cli, which would reject it), an anthropic row is usable too,
// a third driver needs the anthropic_base_url override to be usable at
// all, and claude-cli's own behavior on the same rows is unchanged
// (regression).
func TestResolveRoutePiWireVariants(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{
		{ID: "p1", Name: "anthropic", Kind: "api", Driver: "anthropic",
			CredentialRef: "A_KEY", Enabled: true},
		{ID: "p2", Name: "ollama", Kind: "api", Driver: "openaicompat",
			CredentialRef: "O_KEY", Enabled: true},
		{ID: "p3", Name: "glm-direct", Kind: "api", Driver: "openaicompat",
			CredentialRef: "G_KEY", Enabled: true, AnthropicBaseURL: "https://glm.example/anthropic"},
	}
	routeRows := []RouteRow{
		{Name: "coding-exec", Enabled: true, Chain: []ChainEntry{
			{ProviderID: "p1", Model: "sonnet"},
			{ProviderID: "p2", Model: "qwen"},
			{ProviderID: "p3", Model: "glm-4.7"},
		}},
	}
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })

	// pi axis: anthropic usable+anthropic wire, openaicompat usable+
	// openai wire (pi's whole point), and the override row usable with
	// anthropic wire (the override always exposes an anthropic-format
	// endpoint regardless of the row's own driver).
	piEntries, ok := snap.ResolveRoute("coding-exec", "pi")
	if !ok || len(piEntries) != 3 {
		t.Fatalf("ResolveRoute(pi) = %+v, ok=%v", piEntries, ok)
	}
	if !piEntries[0].Usable || piEntries[0].Wire != "anthropic" {
		t.Fatalf("pi anthropic entry = %+v", piEntries[0])
	}
	if !piEntries[1].Usable || piEntries[1].Wire != "openai" {
		t.Fatalf("pi openaicompat entry = %+v", piEntries[1])
	}
	if piEntries[1].BaseURL != "" {
		t.Fatalf("pi openaicompat entry base_url = %q, want the row's own (empty here)", piEntries[1].BaseURL)
	}
	if !piEntries[2].Usable || piEntries[2].Wire != "anthropic" {
		t.Fatalf("pi override entry = %+v", piEntries[2])
	}
	if piEntries[2].BaseURL != "https://glm.example/anthropic" {
		t.Fatalf("pi override entry base_url = %q, want the override url", piEntries[2].BaseURL)
	}

	// claude-cli axis on the SAME rows: anthropic usable+anthropic
	// wire (regression, unchanged from before pi existed), openaicompat
	// WITHOUT the override is wire-incompatible (claude-cli has no
	// openai wire support), the override row is usable+anthropic wire.
	claudeEntries, ok := snap.ResolveRoute("coding-exec", "claude-cli")
	if !ok || len(claudeEntries) != 3 {
		t.Fatalf("ResolveRoute(claude-cli) = %+v, ok=%v", claudeEntries, ok)
	}
	if !claudeEntries[0].Usable || claudeEntries[0].Wire != "anthropic" {
		t.Fatalf("claude-cli anthropic entry = %+v", claudeEntries[0])
	}
	if claudeEntries[1].Usable {
		t.Fatalf("claude-cli openaicompat entry without override must be unusable: %+v", claudeEntries[1])
	}
	if !strings.Contains(claudeEntries[1].SkipReason, "wire-incompatible") {
		t.Fatalf("claude-cli openaicompat skip reason = %q, want wire-incompatible", claudeEntries[1].SkipReason)
	}
	if !claudeEntries[2].Usable || claudeEntries[2].Wire != "anthropic" {
		t.Fatalf("claude-cli override entry = %+v", claudeEntries[2])
	}
}

// TestResolveRouteKindCliHasNoWire: a kind='cli' row spawns its own CLI
// against the vendor's default endpoint - it has no wire format at
// all, so Wire must stay empty even though it's usable.
func TestResolveRouteKindCliHasNoWire(t *testing.T) {
	t.Parallel()
	snap := harnessSnapshot(t)
	entries, ok := snap.ResolveRoute("coding", "claude-cli")
	if !ok || len(entries) != 2 {
		t.Fatalf("ResolveRoute = %+v, ok=%v", entries, ok)
	}
	if entries[1].Kind != "cli" {
		t.Fatalf("test setup assumption broken: entries[1] = %+v", entries[1])
	}
	if entries[1].Wire != "" {
		t.Fatalf("kind='cli' entry Wire = %q, want empty", entries[1].Wire)
	}
}

// TestResolveRouteCarriesPrices: a delegated executor caller (brain's
// missions harness) needs the entry's own configured price row to cost
// a non-anthropic provider's tokens itself (D-05x) — ResolveRoute must
// carry it, nil when the model has no price row, never guessed.
func TestResolveRouteCarriesPrices(t *testing.T) {
	t.Parallel()
	provRows := []ProviderRow{
		{ID: "p1", Name: "glm", Kind: "api", Driver: "openaicompat",
			CredentialRef: "G_KEY", Enabled: true, AnthropicBaseURL: "http://glm.example",
			Models: []ModelInfo{
				{ID: "glm-4.7", Prices: &ModelPrices{InputPerMTok: 1, OutputPerMTok: 2}},
				{ID: "glm-unpriced"},
			},
		},
	}
	routeRows := []RouteRow{
		{Name: "coding", Enabled: true, Chain: []ChainEntry{
			{ProviderID: "p1", Model: "glm-4.7"},
			{ProviderID: "p1", Model: "glm-unpriced"},
		}},
	}
	snap, _ := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })

	entries, ok := snap.ResolveRoute("coding", "claude-cli")
	if !ok || len(entries) != 2 {
		t.Fatalf("ResolveRoute = %+v, ok=%v, want 2 entries", entries, ok)
	}
	if entries[0].Prices == nil || entries[0].Prices.InputPerMTok != 1 {
		t.Fatalf("priced entry Prices = %+v, want InputPerMTok 1", entries[0].Prices)
	}
	if entries[1].Prices != nil {
		t.Fatalf("unpriced entry Prices = %+v, want nil", entries[1].Prices)
	}
}
