package router

import (
	"testing"

	"github.com/SumonMSelim/timothy/internal/gateway/catalog"
)

func ptrF(v float64) *float64 { return &v }
func ptrB(v bool) *bool       { return &v }

func priced(id string, input float64, mode string, vision bool) catalog.Model {
	m := catalog.Model{ID: id, ModelKey: id, Mode: mode, InputPerMTok: ptrF(input)}
	if vision {
		m.SupportsVision = ptrB(true)
	}
	return m
}

func unpriced(id string, mode string) catalog.Model {
	return catalog.Model{ID: id, ModelKey: id, Mode: mode}
}

func TestBootstrapChainSeedsEmptyRoutes(t *testing.T) {
	p := ProviderRow{ID: "p1"}
	candidates := []catalog.Model{
		priced("cheap", 1, "chat", false),
		priced("pricey", 5, "chat", false),
		priced("embed-1", 0.1, "embedding", false),
	}

	got := BootstrapChain(p, map[string][]ChainEntry{}, candidates)

	for _, route := range []string{"default", "summarize"} {
		chain := got[route]
		if len(chain) != 1 || chain[0].ProviderID != "p1" || chain[0].Model != "cheap" {
			t.Fatalf("%s chain = %+v, want single cheap entry", route, chain)
		}
	}
	if chain := got["embedding"]; len(chain) != 1 || chain[0].Model != "embed-1" {
		t.Fatalf("embedding chain = %+v, want single embed-1 entry", chain)
	}
}

func TestBootstrapChainAppendsAsLastFallback(t *testing.T) {
	p := ProviderRow{ID: "new"}
	candidates := []catalog.Model{priced("m", 2, "chat", false)}
	existing := map[string][]ChainEntry{
		"default": {{ProviderID: "old", Model: "old-model"}},
	}

	got := BootstrapChain(p, existing, candidates)

	chain := got["default"]
	if len(chain) != 2 || chain[0].ProviderID != "old" || chain[1].ProviderID != "new" {
		t.Fatalf("default chain = %+v, want [old, new] with old untouched first", chain)
	}
}

func TestBootstrapChainPicksCheapestCapable(t *testing.T) {
	p := ProviderRow{ID: "p1"}
	candidates := []catalog.Model{
		priced("mid", 3, "chat", false),
		priced("cheapest", 1, "chat", false),
		priced("pricey", 9, "chat", false),
		priced("wrong-cap", 0.01, "embedding", false), // cheaper but can't serve chat
	}

	got := BootstrapChain(p, map[string][]ChainEntry{}, candidates)

	if chain := got["default"]; len(chain) != 1 || chain[0].Model != "cheapest" {
		t.Fatalf("default chain = %+v, want cheapest", chain)
	}
}

func TestBootstrapChainUnpricedModelsSortLast(t *testing.T) {
	p := ProviderRow{ID: "p1"}
	candidates := []catalog.Model{
		unpriced("unknown-cost", "chat"),
		priced("known-cheap", 1, "chat", false),
	}

	got := BootstrapChain(p, map[string][]ChainEntry{}, candidates)

	if chain := got["default"]; len(chain) != 1 || chain[0].Model != "known-cheap" {
		t.Fatalf("default chain = %+v, want known-cheap over unpriced", chain)
	}
}

func TestBootstrapChainTiesBreakOnModelID(t *testing.T) {
	p := ProviderRow{ID: "p1"}
	candidates := []catalog.Model{
		priced("z-model", 1, "chat", false),
		priced("a-model", 1, "chat", false),
	}

	got := BootstrapChain(p, map[string][]ChainEntry{}, candidates)

	if chain := got["default"]; len(chain) != 1 || chain[0].Model != "a-model" {
		t.Fatalf("default chain = %+v, want deterministic a-model tie-break", chain)
	}
}

func TestBootstrapChainOmitsRouteWithNoCapableModel(t *testing.T) {
	p := ProviderRow{ID: "p1"}
	candidates := []catalog.Model{priced("chat-only", 1, "chat", false)}

	got := BootstrapChain(p, map[string][]ChainEntry{}, candidates)

	if _, ok := got["embedding"]; ok {
		t.Fatalf("embedding present = %+v, want omitted (no embeddings-capable model)", got["embedding"])
	}
	if _, ok := got["default"]; !ok {
		t.Fatal("default omitted despite a capable model")
	}
}

func TestBootstrapChainSkipsExistingDuplicate(t *testing.T) {
	p := ProviderRow{ID: "p1"}
	candidates := []catalog.Model{priced("m", 1, "chat", false)}
	existing := map[string][]ChainEntry{
		"default": {{ProviderID: "p1", Model: "m"}},
	}

	got := BootstrapChain(p, existing, candidates)

	if _, ok := got["default"]; ok {
		t.Fatalf("default present = %+v, want omitted (already chained)", got["default"])
	}
}

func TestBootstrapChainNeverMutatesExistingSlice(t *testing.T) {
	existingChain := []ChainEntry{{ProviderID: "old", Model: "old-model"}}
	existing := map[string][]ChainEntry{"default": existingChain}
	p := ProviderRow{ID: "new"}
	candidates := []catalog.Model{priced("m", 1, "chat", false)}

	BootstrapChain(p, existing, candidates)

	if len(existingChain) != 1 {
		t.Fatalf("caller's chain slice mutated: %+v", existingChain)
	}
}

func TestBootstrapChainExcludedProviderYieldsNoUpdates(t *testing.T) {
	p := ProviderRow{ID: "ollama", ExcludeFromBootstrap: true}
	candidates := []catalog.Model{priced("qwen2.5:7b", 0, "chat", false)}

	got := BootstrapChain(p, map[string][]ChainEntry{}, candidates)

	if got != nil {
		t.Fatalf("BootstrapChain(excluded) = %+v, want nil (no route touched)", got)
	}
}

// TestBootstrapChainCliKindYieldsNoUpdates confirms a kind='cli' row
// (subscription-harness providers like claude-cli/codex-cli) never
// bootstraps into chat routes: those rows serve no chat traffic
// (BuildSnapshot skips them), so appending them here only pollutes
// chains with catalog junk.
func TestBootstrapChainCliKindYieldsNoUpdates(t *testing.T) {
	p := ProviderRow{ID: "cli1", Kind: "cli"}
	candidates := []catalog.Model{priced("m", 1, "chat", false)}

	got := BootstrapChain(p, map[string][]ChainEntry{}, candidates)

	if got != nil {
		t.Fatalf("BootstrapChain(kind=cli) = %+v, want nil (no route touched)", got)
	}
}

// TestBootstrapChainSeedsVisionRoute confirms a newly connected
// vision-capable provider auto-chains into the "vision" route (D-046),
// the same as the other fixed bootstrap routes.
func TestBootstrapChainSeedsVisionRoute(t *testing.T) {
	p := ProviderRow{ID: "p1"}
	candidates := []catalog.Model{
		priced("cheap-vision", 2, "chat", true),
		priced("pricey-vision", 8, "chat", true),
	}

	got := BootstrapChain(p, map[string][]ChainEntry{}, candidates)

	chain := got["vision"]
	if len(chain) != 1 || chain[0].ProviderID != "p1" || chain[0].Model != "cheap-vision" {
		t.Fatalf("vision chain = %+v, want single cheapest vision-capable entry", chain)
	}
}

// TestBootstrapChainOmitsVisionForNonVisionProvider confirms a provider
// with no vision-capable model never touches the "vision" route.
func TestBootstrapChainOmitsVisionForNonVisionProvider(t *testing.T) {
	p := ProviderRow{ID: "p1"}
	candidates := []catalog.Model{priced("chat-only", 1, "chat", false)}

	got := BootstrapChain(p, map[string][]ChainEntry{}, candidates)

	if _, ok := got["vision"]; ok {
		t.Fatalf("vision present = %+v, want omitted (no vision-capable model)", got["vision"])
	}
}

func TestBootstrapChainIgnoresResearchRoute(t *testing.T) {
	p := ProviderRow{ID: "p1"}
	candidates := []catalog.Model{priced("m", 1, "chat", false)}

	got := BootstrapChain(p, map[string][]ChainEntry{"research": {}}, candidates)

	if _, ok := got["research"]; ok {
		t.Fatal("research route bootstrapped; it must stay hand-configured")
	}
}
