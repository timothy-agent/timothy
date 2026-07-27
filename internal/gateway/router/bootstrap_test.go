package router

import "testing"

func priced(id string, input float64, caps ...string) ModelInfo {
	return ModelInfo{ID: id, Capabilities: caps, Prices: &ModelPrices{InputPerMTok: input}}
}

func unpriced(id string, caps ...string) ModelInfo {
	return ModelInfo{ID: id, Capabilities: caps}
}

func TestBootstrapChainSeedsEmptyRoutes(t *testing.T) {
	p := ProviderRow{ID: "p1", Models: []ModelInfo{
		priced("cheap", 1, "chat", "streaming"),
		priced("pricey", 5, "chat", "streaming"),
		priced("embed-1", 0.1, "embeddings"),
	}}

	got := BootstrapChain(p, map[string][]ChainEntry{})

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
	p := ProviderRow{ID: "new", Models: []ModelInfo{priced("m", 2, "chat")}}
	existing := map[string][]ChainEntry{
		"default": {{ProviderID: "old", Model: "old-model"}},
	}

	got := BootstrapChain(p, existing)

	chain := got["default"]
	if len(chain) != 2 || chain[0].ProviderID != "old" || chain[1].ProviderID != "new" {
		t.Fatalf("default chain = %+v, want [old, new] with old untouched first", chain)
	}
}

func TestBootstrapChainPicksCheapestCapable(t *testing.T) {
	p := ProviderRow{ID: "p1", Models: []ModelInfo{
		priced("mid", 3, "chat"),
		priced("cheapest", 1, "chat"),
		priced("pricey", 9, "chat"),
		priced("wrong-cap", 0.01, "embeddings"), // cheaper but can't serve chat
	}}

	got := BootstrapChain(p, map[string][]ChainEntry{})

	if chain := got["default"]; len(chain) != 1 || chain[0].Model != "cheapest" {
		t.Fatalf("default chain = %+v, want cheapest", chain)
	}
}

func TestBootstrapChainUnpricedModelsSortLast(t *testing.T) {
	p := ProviderRow{ID: "p1", Models: []ModelInfo{
		unpriced("unknown-cost", "chat"),
		priced("known-cheap", 1, "chat"),
	}}

	got := BootstrapChain(p, map[string][]ChainEntry{})

	if chain := got["default"]; len(chain) != 1 || chain[0].Model != "known-cheap" {
		t.Fatalf("default chain = %+v, want known-cheap over unpriced", chain)
	}
}

func TestBootstrapChainTiesBreakOnModelID(t *testing.T) {
	p := ProviderRow{ID: "p1", Models: []ModelInfo{
		priced("z-model", 1, "chat"),
		priced("a-model", 1, "chat"),
	}}

	got := BootstrapChain(p, map[string][]ChainEntry{})

	if chain := got["default"]; len(chain) != 1 || chain[0].Model != "a-model" {
		t.Fatalf("default chain = %+v, want deterministic a-model tie-break", chain)
	}
}

func TestBootstrapChainOmitsRouteWithNoCapableModel(t *testing.T) {
	p := ProviderRow{ID: "p1", Models: []ModelInfo{priced("chat-only", 1, "chat")}}

	got := BootstrapChain(p, map[string][]ChainEntry{})

	if _, ok := got["embedding"]; ok {
		t.Fatalf("embedding present = %+v, want omitted (no embeddings-capable model)", got["embedding"])
	}
	if _, ok := got["default"]; !ok {
		t.Fatal("default omitted despite a capable model")
	}
}

func TestBootstrapChainSkipsExistingDuplicate(t *testing.T) {
	p := ProviderRow{ID: "p1", Models: []ModelInfo{priced("m", 1, "chat")}}
	existing := map[string][]ChainEntry{
		"default": {{ProviderID: "p1", Model: "m"}},
	}

	got := BootstrapChain(p, existing)

	if _, ok := got["default"]; ok {
		t.Fatalf("default present = %+v, want omitted (already chained)", got["default"])
	}
}

func TestBootstrapChainNeverMutatesExistingSlice(t *testing.T) {
	existingChain := []ChainEntry{{ProviderID: "old", Model: "old-model"}}
	existing := map[string][]ChainEntry{"default": existingChain}
	p := ProviderRow{ID: "new", Models: []ModelInfo{priced("m", 1, "chat")}}

	BootstrapChain(p, existing)

	if len(existingChain) != 1 {
		t.Fatalf("caller's chain slice mutated: %+v", existingChain)
	}
}

func TestBootstrapChainExcludedProviderYieldsNoUpdates(t *testing.T) {
	p := ProviderRow{ID: "ollama", ExcludeFromBootstrap: true, Models: []ModelInfo{
		priced("qwen2.5:7b", 0, "chat"),
	}}

	got := BootstrapChain(p, map[string][]ChainEntry{})

	if got != nil {
		t.Fatalf("BootstrapChain(excluded) = %+v, want nil (no route touched)", got)
	}
}

func TestBootstrapChainIgnoresResearchRoute(t *testing.T) {
	p := ProviderRow{ID: "p1", Models: []ModelInfo{priced("m", 1, "chat")}}

	got := BootstrapChain(p, map[string][]ChainEntry{"research": {}})

	if _, ok := got["research"]; ok {
		t.Fatal("research route bootstrapped; it must stay hand-configured")
	}
}
