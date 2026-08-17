import type { CatalogPrice, UnpricedGroup } from '../api/types'

// priceKey matches the key catalogPrices' caller builds from an
// UnpricedGroup pair — provider alongside model, so a model name that
// collides across vendors (e.g. a free zai model matching a priced
// cloudflare one of the same segment) can never borrow the wrong
// provider's price.
function priceKey(provider: string, model: string): string {
  return `${provider} ${model}`
}

// estimateUnpriced prices unpriced (provider, model) groups from prices
// (fetched by the caller via catalogPrices() for exactly those pairs,
// each resolved within its own provider's catalog candidates). The
// ledger's cost stays the honest record (unknown price = NULL, never
// guessed server-side); this is a display hint for what the unpriced
// calls would roughly cost. Estimates are keyed by model alone (the
// display groups by model, not provider) so a model served by more than
// one provider sums its estimate across them. A pair with no priced
// match contributes nothing — the estimate is itself a floor.
export function estimateUnpriced(
  groups: UnpricedGroup[],
  prices: CatalogPrice[],
): Map<string, number> {
  const byPair = new Map<string, CatalogPrice>()
  for (const p of prices) byPair.set(priceKey(p.provider, p.model), p)

  const out = new Map<string, number>()
  for (const g of groups) {
    const price = byPair.get(priceKey(g.provider, g.model))?.price
    if (price?.input_per_mtok == null || price.output_per_mtok == null) continue
    const est =
      (g.unpriced_input_tokens * price.input_per_mtok +
        g.unpriced_output_tokens * price.output_per_mtok) /
      1_000_000
    if (est <= 0) continue
    out.set(g.model, (out.get(g.model) ?? 0) + est)
  }
  return out
}

export function totalEstimate(estimates: Map<string, number>): number {
  let sum = 0
  for (const v of estimates.values()) sum += v
  return sum
}
