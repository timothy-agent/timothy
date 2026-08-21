package admin

import (
	"context"
	"fmt"

	"github.com/SumonMSelim/timothy/internal/gateway/catalog"
)

// CatalogRefresh fetches and syncs the model catalog now, audited so a
// manual refresh shows in the trail (the sweep's own periodic syncs
// don't — they're not an operator action).
func (a *Admin) CatalogRefresh(ctx context.Context) (catalog.SyncStatus, error) {
	st, err := a.catalog.Sync(ctx)
	if err != nil {
		a.audit(ctx, "refresh", "model_catalog", "", nil, map[string]string{"error": err.Error()})
		return catalog.SyncStatus{}, err
	}
	a.audit(ctx, "refresh", "model_catalog", "", nil, st)
	return st, nil
}

// CatalogStatus reports the last sync attempt.
func (a *Admin) CatalogStatus(ctx context.Context) (catalog.SyncStatus, error) {
	return a.catalog.Status(ctx)
}

// CatalogSearch searches the cached catalog.
func (a *Admin) CatalogSearch(ctx context.Context, q, litellmProvider string, limit int) ([]catalog.Model, error) {
	return a.catalog.Search(ctx, q, litellmProvider, limit)
}

// CatalogModelsForProvider searches the catalog restricted to a
// provider row's candidate litellm_provider(s) — the model id picker
// on a provider's form, offering real catalog ids beyond what's
// already declared. A kind='cli' row has no chat driver (D-051), but
// claude-cli specifically talks Anthropic's own API under the hood, so
// it maps to the "anthropic" catalog provider rather than falling back
// to an unrestricted search; other cli drivers (codex-cli) get no
// restriction, same as an unrecognized api driver/host.
func (a *Admin) CatalogModelsForProvider(ctx context.Context, id, q string, limit int) ([]catalog.Model, error) {
	p, err := a.get(ctx, id)
	if err != nil {
		return nil, err
	}
	models, err := a.catalog.SearchProviders(ctx, q, catalog.CandidateProvidersForRow(p.Kind, p.Driver, p.BaseURL, p.Options), limit)
	if err != nil {
		return nil, fmt.Errorf("admin catalog models: %w", err)
	}
	return models, nil
}

// ProviderModel is one (provider row name, model id) pair CatalogPrices
// is asked to price — provider is the providers row's name exactly as
// recorded in cost_ledger.provider, so a caller reading ledger rows can
// pass them straight through.
type ProviderModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// PricedModel pairs one requested ProviderModel with its resolved
// catalog price, nil when the provider name is unknown or the model
// doesn't match within that provider's candidate pool (never matched
// against another vendor's catalog rows — see D-0XX, the glm-4.7-flash/
// cloudflare mismatch this replaces).
type PricedModel struct {
	Provider string         `json:"provider"`
	Model    string         `json:"model"`
	Price    *catalog.Model `json:"price"`
}

// CatalogPrices resolves each requested (provider, model) pair within
// that PROVIDER's own candidate litellm_provider(s) only — never the
// whole catalog — so a model name that happens to collide with another
// vendor's catalog entry (e.g. a free zai model name matching a priced
// cloudflare one) can never borrow that vendor's price. provider must
// be a providers row's name (as stored in cost_ledger.provider); an
// unknown name, or a model with no match inside that provider's
// candidates, both report a nil price for that pair — never a fallback
// to an unrestricted search. Provider rows are looked up by name once
// each (cached across pairs sharing a provider) and handed to
// resolvePricedModel, which does the actual candidate-restricted match
// and is unit-tested without a DB.
func (a *Admin) CatalogPrices(ctx context.Context, pairs []ProviderModel) ([]PricedModel, error) {
	out := make([]PricedModel, len(pairs))
	providers := map[string]*Provider{} // name -> row, nil means unknown/lookup failed
	for i, pair := range pairs {
		p, ok := providers[pair.Provider]
		if !ok {
			row, err := a.getByName(ctx, pair.Provider)
			if err != nil {
				p = nil
			} else {
				p = &row
			}
			providers[pair.Provider] = p
		}
		priced, err := resolvePricedModel(ctx, a.catalog, pair, p)
		if err != nil {
			return nil, fmt.Errorf("admin catalog prices: %w", err)
		}
		out[i] = priced
	}
	return out, nil
}

// catalogSuggester is the slice of *catalog.Store resolvePricedModel
// needs — an interface at point of use so tests can fake it without a
// synced catalog.
type catalogSuggester interface {
	Suggest(ctx context.Context, candidates []string, modelIDs []string) ([]catalog.Suggestion, error)
}

// resolvePricedModel matches pair.Model against cat, restricted to p's
// candidate litellm_provider(s) (candidateProvidersForRow). A nil p
// (unknown provider name) or no priced match within its candidates both
// report a nil Price — the pure decision logic CatalogPrices' DB lookup
// wraps, unit-testable without a DB.
func resolvePricedModel(ctx context.Context, cat catalogSuggester, pair ProviderModel, p *Provider) (PricedModel, error) {
	out := PricedModel{Provider: pair.Provider, Model: pair.Model}
	if p == nil {
		return out, nil
	}

	candidates := catalog.CandidateProvidersForRow(p.Kind, p.Driver, p.BaseURL, p.Options)
	sugs, err := cat.Suggest(ctx, candidates, []string{pair.Model})
	if err != nil {
		return PricedModel{}, err
	}
	if len(sugs) != 1 || sugs[0].InputPerMTok == nil || sugs[0].OutputPerMTok == nil {
		return out, nil
	}
	sg := sugs[0]
	out.Price = &catalog.Model{
		ID:                sg.Match,
		ModelKey:          sg.Match,
		MaxInputTokens:    sg.MaxInputTokens,
		MaxOutputTokens:   sg.MaxOutputTokens,
		InputPerMTok:      sg.InputPerMTok,
		OutputPerMTok:     sg.OutputPerMTok,
		CacheReadPerMTok:  sg.CacheReadPerMTok,
		CacheWritePerMTok: sg.CacheWritePerMTok,
	}
	return out, nil
}

