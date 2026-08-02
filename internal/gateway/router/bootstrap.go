package router

import (
	"math"
	"sort"
)

// SystemRoles are the roles Timothy requires to work at all — chat
// (role "default"), the session/turn-memory summarizer (role
// "summarize"), embeddings (role "embedding"), and vision (role
// "vision", D-046) — each paired with the driver capability its
// bound route must serve. A newly connected provider auto-fills
// whichever of these roles it can. Any other route (e.g. a hand-made
// "research" or "coding" route) is plain user configuration and is
// never auto-filled.
var SystemRoles = []struct{ Role, Capability string }{
	{"default", "chat"},
	{"summarize", "chat"},
	{"embedding", "embeddings"},
	{"vision", "vision"},
}

// BootstrapChain computes role -> new-chain-entry for a newly
// connected (or re-saved) provider, applied to the role's CURRENT
// chain passed in via existing (keyed by role, not route name — the
// caller resolves each role to its bound route's chain before
// calling this). A role with no capable model in the new provider is
// omitted from the result — callers apply only what's returned, so
// unrelated roles are untouched.
//
// Rule (D-033 follow-up): an empty chain is seeded with the provider's
// cheapest capable model; a non-empty chain gets that model appended
// as the LAST entry — existing priority order is never reordered or
// removed, so hand-tuned chains only ever gain a fallback.
func BootstrapChain(p ProviderRow, existing map[string][]ChainEntry) map[string][]ChainEntry {
	if p.ExcludeFromBootstrap {
		return nil
	}
	out := map[string][]ChainEntry{}
	for _, sr := range SystemRoles {
		model, ok := cheapestCapable(p.Models, sr.Capability)
		if !ok {
			continue
		}
		entry := ChainEntry{ProviderID: p.ID, Model: model}
		chain := existing[sr.Role]
		if alreadyChained(chain, p.ID, model) {
			continue
		}
		next := make([]ChainEntry, len(chain), len(chain)+1)
		copy(next, chain)
		out[sr.Role] = append(next, entry)
	}
	return out
}

// cheapestCapable returns the id of the cheapest model declaring cap,
// by input price per million tokens. Models without a declared price
// sort last (unknown cost is never assumed cheapest). Ties break on
// model id for a deterministic result.
func cheapestCapable(models []ModelInfo, cap string) (string, bool) {
	var candidates []ModelInfo
	for _, m := range models {
		for _, c := range m.Capabilities {
			if c == cap {
				candidates = append(candidates, m)
				break
			}
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.Slice(candidates, func(i, j int) bool {
		pi, pj := price(candidates[i]), price(candidates[j])
		if pi != pj {
			return pi < pj
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates[0].ID, true
}

// price returns a model's input cost, or +Inf when undeclared so it
// never outranks a model with a known price.
func price(m ModelInfo) float64 {
	if m.Prices == nil {
		return math.Inf(1)
	}
	return m.Prices.InputPerMTok
}

// alreadyChained reports whether the chain already carries this
// provider+model pair — bootstrap must not append a duplicate on a
// repeated save.
func alreadyChained(chain []ChainEntry, providerID, model string) bool {
	for _, e := range chain {
		if e.ProviderID == providerID && e.Model == model {
			return true
		}
	}
	return false
}
