package router

import (
	"math"
	"sort"

	"github.com/SumonMSelim/timothy/internal/gateway/catalog"
)

// SystemRoles are the roles Timothy requires to work at all — chat
// (role "default"), the session/turn-memory summarizer (role
// "summarize"), embeddings (role "embedding"), and vision (role
// "vision", D-046), and fixed-choice decisions (role "decide", D-163)
// — each paired with the driver capability its bound route must
// serve. A newly connected provider auto-fills whichever of these
// roles it can. Any other route (e.g. a hand-made "research" or
// "coding" route) is plain user configuration and is never
// auto-filled.
var SystemRoles = []struct{ Role, Capability string }{
	{"default", "chat"},
	{"summarize", "chat"},
	{"embedding", "embeddings"},
	{"vision", "vision"},
	{"decide", "decide"},
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
// removed, so hand-tuned chains only ever gain a fallback. candidates
// is the caller's catalog search restricted to p's candidate
// litellm_provider(s) (catalog.CandidateProvidersForRow) — kept as a
// plain slice rather than a *catalog.Store dependency so this stays
// pure and unit-testable with plain fixtures. A kind="cli" row never
// serves chat routes, so it is excluded from bootstrap like
// ExcludeFromBootstrap.
func BootstrapChain(p ProviderRow, existing map[string][]ChainEntry, candidates []catalog.Model) map[string][]ChainEntry {
	if p.ExcludeFromBootstrap || p.Kind == "cli" {
		return nil
	}
	if decisionDrivers[p.Driver] {
		return bootstrapDecision(p, existing["decide"])
	}
	out := map[string][]ChainEntry{}
	for _, sr := range SystemRoles {
		model, ok := CheapestCapable(candidates, sr.Capability)
		// The operator validated default_model against the live
		// provider; the catalog's cheapest chat row may not exist there
		// (an Ollama host serves only what is pulled).
		if servedByChat(sr.Capability) && defaultModelServesChat(p.DefaultModel, candidates) {
			model, ok = p.DefaultModel, true
		}
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

// decisionDrivers are drivers that serve the decide role only (D-164):
// they never chat, so bootstrap keeps them off every other role.
var decisionDrivers = map[string]bool{"laya": true}

// bootstrapDecision chains a decision model FIRST on the decide route:
// the chat entries already there are the llm fallback, and a fallback
// behind the real thing is the only order in which the decision model
// ever answers. A row with no default model has nothing to chain; a
// row already chained is left alone.
func bootstrapDecision(p ProviderRow, existing []ChainEntry) map[string][]ChainEntry {
	if p.DefaultModel == "" || alreadyChained(existing, p.ID, p.DefaultModel) {
		return nil
	}
	chain := make([]ChainEntry, 0, len(existing)+1)
	chain = append(chain, ChainEntry{ProviderID: p.ID, Model: p.DefaultModel})
	chain = append(chain, existing...)
	return map[string][]ChainEntry{"decide": chain}
}

// CheapestCapable returns the id of the cheapest catalog model
// declaring cap ("chat", "embeddings", or "vision"), by input price
// per million tokens. Models without a declared price sort last
// (unknown cost is never assumed cheapest). Ties break on model id
// for a deterministic result. Shared by BootstrapChain and admin's
// default_model auto-seed at provider creation.
func CheapestCapable(models []catalog.Model, cap string) (string, bool) {
	var candidates []catalog.Model
	for _, m := range models {
		if modelHasCapability(m, cap) {
			candidates = append(candidates, m)
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.Slice(candidates, func(i, j int) bool {
		pi, pj := catalogPrice(candidates[i]), catalogPrice(candidates[j])
		if pi != pj {
			return pi < pj
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates[0].ID, true
}

// modelHasCapability derives a catalog model's capability from its
// Mode ("embedding" -> embeddings) and SupportsVision field (vision);
// any other mode (typically "chat") counts as chat-capable, and also
// as vision-capable when SupportsVision is true. No catalog model is a
// decision model, so "decide" bootstraps onto a chat model (D-163).
func modelHasCapability(m catalog.Model, cap string) bool {
	switch cap {
	case "embeddings":
		return m.Mode == "embedding"
	case "vision":
		return m.SupportsVision != nil && *m.SupportsVision
	default: // "chat", "decide"
		return m.Mode != "embedding"
	}
}

// servedByChat reports whether a role's capability bootstraps onto the
// provider's chat model: chat itself, and decide through the llm
// fallback (D-163).
func servedByChat(cap string) bool {
	return cap == "chat" || cap == "decide"
}

// catalogPrice returns a catalog model's input cost, or +Inf when
// undeclared so it never outranks a model with a known price.
func catalogPrice(m catalog.Model) float64 {
	if m.InputPerMTok == nil {
		return math.Inf(1)
	}
	return *m.InputPerMTok
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

// defaultModelServesChat reports whether a provider's default_model can
// seed the chat roles: set, and not a catalog embedding model. A model
// the catalog does not know counts as chat.
func defaultModelServesChat(model string, candidates []catalog.Model) bool {
	if model == "" {
		return false
	}
	for _, m := range candidates {
		if m.ID == model || m.ModelKey == model {
			return modelHasCapability(m, "chat")
		}
	}
	return true
}
