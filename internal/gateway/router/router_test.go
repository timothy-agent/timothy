package router

import (
	"errors"
	"strings"
	"testing"
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
		{TaskCategory: "coding", Chain: []ChainEntry{
			{ProviderID: "p1", Model: "sonnet"},
			{ProviderID: "p2", Model: "grok-4"},
		}, Enabled: true},
		{TaskCategory: "mini", Chain: []ChainEntry{
			{ProviderID: "p3", Model: "m"},
		}, Enabled: true},
		{TaskCategory: "off", Chain: []ChainEntry{
			{ProviderID: "p1", Model: "sonnet"},
		}, Enabled: false},
		{TaskCategory: "ghost", Chain: []ChainEntry{
			{ProviderID: "nope", Model: "m"},
		}, Enabled: true},
		{TaskCategory: "embedding", Chain: []ChainEntry{
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

	attempts, err := snap.Resolve("coding", "")
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

	attempts, err := snap.Resolve("coding", "grok")
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

	attempts, err := snap.Resolve("coding", "opus")
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

	attempts, err := snap.Resolve("coding", "no-such-thing")
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

	attempts, err := snap.Resolve("coding", "")
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
		_, err := snap.Resolve(tt.category, "")
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

	attempts, err := snap.Resolve("embedding", "")
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

	_, err := snap.Resolve("embedding", "")
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
		{TaskCategory: "coding", Chain: []ChainEntry{
			{ProviderID: "p1", Model: "titan-embed"}, // embeddings-only: skip
			{ProviderID: "p1", Model: "nova"},
			{ProviderID: "p1", Model: "undeclared"}, // driver decides: keep
		}, Enabled: true},
		{TaskCategory: "embedding", Chain: []ChainEntry{
			{ProviderID: "p1", Model: "nova"}, // chat-only model: skip
			{ProviderID: "p1", Model: "titan-embed"},
		}, Enabled: true},
	}
	snap, err := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	attempts, err := snap.Resolve("coding", "")
	if err != nil {
		t.Fatalf("Resolve coding: %v", err)
	}
	if got := attemptNames(attempts); got != "bedrock/nova,bedrock/undeclared" {
		t.Fatalf("coding attempts = %s", got)
	}

	attempts, err = snap.Resolve("embedding", "")
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
	routeRows := []RouteRow{{TaskCategory: "coding", Chain: []ChainEntry{
		{ProviderID: "p1", Model: "titan-embed"},
	}, Enabled: true}}
	snap, err := BuildSnapshot(provRows, routeRows, func(string) string { return "sk" })
	if err != nil {
		t.Fatalf("BuildSnapshot: %v", err)
	}

	_, err = snap.Resolve("coding", "")
	if err == nil || !strings.Contains(err.Error(), "bedrock/titan-embed (lacks chat capability") {
		t.Fatalf("err = %v, want model-naming capability reason", err)
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
