import { describe, expect, it } from 'vitest'
import type { UsagePoint } from '../api/types'
import { estimateUnpriced, totalEstimate } from './costEstimate'

function point(overrides: Partial<UsagePoint>): UsagePoint {
  return {
    bucket: '2026-07-24T00:00:00Z',
    group: 'gpt-5.6-sol',
    currency: 'USD',
    cost: 0,
    input_tokens: 0,
    output_tokens: 0,
    requests: 1,
    errors: 0,
    unpriced_input_tokens: 0,
    unpriced_output_tokens: 0,
    ...overrides,
  }
}

describe('estimateUnpriced', () => {
  it('prices unpriced tokens from the catalog per model', () => {
    // gpt-5.6-sol: $5/mtok in, $30/mtok out in the catalog.
    const est = estimateUnpriced([
      point({ unpriced_input_tokens: 1_000_000, unpriced_output_tokens: 100_000 }),
      point({ unpriced_input_tokens: 1_000_000 }),
    ])
    expect(est.get('gpt-5.6-sol')).toBeCloseTo(5 + 3 + 5, 6)
  })

  it('skips models absent from the catalog and priced usage', () => {
    const est = estimateUnpriced([
      point({ group: 'my-local-finetune', unpriced_input_tokens: 5_000_000 }),
      point({ cost: 1.23, input_tokens: 100_000 }), // priced: no unpriced tokens
    ])
    expect(est.size).toBe(0)
    expect(totalEstimate(est)).toBe(0)
  })

  it('totals across models', () => {
    const est = estimateUnpriced([
      point({ unpriced_output_tokens: 1_000_000 }), // $30
      point({ group: 'gpt-5.6-sol-pro', unpriced_input_tokens: 2_000_000 }), // $10
    ])
    expect(totalEstimate(est)).toBeCloseTo(40, 6)
  })
})
