import { describe, expect, it } from 'vitest'
import type { CatalogPrice, UnpricedGroup } from '../api/types'
import { estimateUnpriced, totalEstimate } from './costEstimate'

function group(overrides: Partial<UnpricedGroup>): UnpricedGroup {
  return {
    provider: 'openai',
    model: 'gpt-5.6-sol',
    unpriced_input_tokens: 0,
    unpriced_output_tokens: 0,
    ...overrides,
  }
}

// prices fixture mirrors what CatalogPrices resolves for these
// provider/model pairs: gpt-5.6-sol (openai) $5/mtok in, $30/mtok out;
// gpt-5.6-sol-pro (openai) same.
const prices: CatalogPrice[] = [
  { provider: 'openai', model: 'gpt-5.6-sol', price: { input_per_mtok: 5, output_per_mtok: 30 } },
  { provider: 'openai', model: 'gpt-5.6-sol-pro', price: { input_per_mtok: 5, output_per_mtok: 30 } },
]

describe('estimateUnpriced', () => {
  it('prices unpriced tokens from the catalog per model', () => {
    const est = estimateUnpriced(
      [
        group({ unpriced_input_tokens: 1_000_000, unpriced_output_tokens: 100_000 }),
        group({ unpriced_input_tokens: 1_000_000 }),
      ],
      prices,
    )
    expect(est.get('gpt-5.6-sol')).toBeCloseTo(5 + 3 + 5, 6)
  })

  it('skips models absent from the catalog and priced usage', () => {
    const est = estimateUnpriced(
      [
        group({ model: 'my-local-finetune', unpriced_input_tokens: 5_000_000 }),
        group({ unpriced_input_tokens: 0, unpriced_output_tokens: 0 }), // priced: no unpriced tokens
      ],
      prices,
    )
    expect(est.size).toBe(0)
    expect(totalEstimate(est)).toBe(0)
  })

  it('skips a model the catalog matched but left unpriced (null)', () => {
    const est = estimateUnpriced(
      [group({ model: 'gpt-4o-unpriced', unpriced_input_tokens: 1_000_000 })],
      [{ provider: 'openai', model: 'gpt-4o-unpriced', price: null }],
    )
    expect(est.size).toBe(0)
  })

  it('never borrows another provider\'s price for the same model segment', () => {
    const est = estimateUnpriced(
      [group({ provider: 'zai', model: 'glm-4.7-flash', unpriced_input_tokens: 1_000_000 })],
      [{ provider: 'cloudflare', model: 'glm-4.7-flash', price: { input_per_mtok: 5, output_per_mtok: 30 } }],
    )
    expect(est.size).toBe(0)
  })

  it('totals across models', () => {
    const est = estimateUnpriced(
      [
        group({ unpriced_output_tokens: 1_000_000 }), // $30
        group({ model: 'gpt-5.6-sol-pro', unpriced_input_tokens: 2_000_000 }), // $10
      ],
      prices,
    )
    expect(totalEstimate(est)).toBeCloseTo(40, 6)
  })
})
