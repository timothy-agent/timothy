import { modelCatalog } from '../components/settings/modelCatalog'
import type { UsagePoint } from '../api/types'

// catalogPrices flattens the advisory model catalog into one id→price
// lookup. When the same model id appears under several providers the
// first entry wins — the catalog keeps duplicates price-identical.
const catalogPrices = (() => {
  const m = new Map<string, { input: number; output: number }>()
  for (const models of Object.values(modelCatalog)) {
    for (const model of models) {
      if (model.prices && !m.has(model.id)) {
        m.set(model.id, {
          input: model.prices.input_per_mtok,
          output: model.prices.output_per_mtok,
        })
      }
    }
  }
  return m
})()

// estimateUnpriced prices a model-grouped series' unpriced tokens from
// the advisory catalog. The ledger's cost_usd stays the honest record
// (unknown price = NULL, never guessed server-side); this is a display
// hint for what the unpriced calls would roughly cost. Models absent
// from the catalog contribute nothing — the estimate is itself a floor.
export function estimateUnpriced(byModel: UsagePoint[]): Map<string, number> {
  const out = new Map<string, number>()
  for (const p of byModel) {
    const price = catalogPrices.get(p.group)
    if (!price) continue
    const est =
      (p.unpriced_input_tokens * price.input + p.unpriced_output_tokens * price.output) / 1_000_000
    if (est <= 0) continue
    out.set(p.group, (out.get(p.group) ?? 0) + est)
  }
  return out
}

export function totalEstimate(estimates: Map<string, number>): number {
  let sum = 0
  for (const v of estimates.values()) sum += v
  return sum
}
