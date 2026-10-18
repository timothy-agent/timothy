import { describe, expect, it } from 'vitest'
import type { AdminProvider } from '../../api/types'
import {
  optionsWithPrices,
  parseOperatorPrices,
  parsePrice,
  unpricedListed,
  withListedFree,
  withoutPrice,
  withPrice,
} from './modelPrices'

const provider: AdminProvider = {
  id: 'p1',
  name: 'mars-ai',
  kind: 'api',
  driver: 'openaicompat',
  base_url: 'http://mars.local/v1',
  default_model: 'm1',
  credential_ref: '',
  headers: {},
  enabled: true,
  options: { request_timeout: '5m', litellm_provider: 'zai' },
}

describe('parseOperatorPrices', () => {
  it.each([
    ['absent', undefined, {}],
    ['empty', '', {}],
    ['not json', 'nope', {}],
    ['an array', '[1]', {}],
    ['an object', '{"m1":{"input_per_mtok":1,"output_per_mtok":2}}', { m1: { input_per_mtok: 1, output_per_mtok: 2 } }],
  ])('%s', (_, raw, want) => {
    expect(parseOperatorPrices(raw)).toEqual(want)
  })
})

describe('price edits', () => {
  it('withPrice sets input and output and keeps other keys of the entry', () => {
    const prices = { m1: { input_per_mtok: 1, output_per_mtok: 2, cache_read_per_mtok: 0.1 } }
    expect(withPrice(prices, 'm1', 3, 4)).toEqual({ m1: { input_per_mtok: 3, output_per_mtok: 4, cache_read_per_mtok: 0.1 } })
    expect(withPrice(prices, 'm2', 0, 0)).toEqual({ ...prices, m2: { input_per_mtok: 0, output_per_mtok: 0 } })
  })

  it('withoutPrice drops only that model', () => {
    const prices = { m1: { input_per_mtok: 1 }, m2: { input_per_mtok: 2 } }
    expect(withoutPrice(prices, 'm1')).toEqual({ m2: { input_per_mtok: 2 } })
    expect(prices).toHaveProperty('m1')
  })

  it('withListedFree prices every listed model without an operator price at 0', () => {
    const prices = { m1: { input_per_mtok: 1, output_per_mtok: 2 } }
    const models = [
      { id: 'm1' },
      { id: 'm2', input_per_mtok: 5, output_per_mtok: 6, price_source: 'catalog' as const },
      { id: 'm3' },
    ]
    expect(unpricedListed(prices, models)).toEqual(['m2', 'm3'])
    expect(withListedFree(prices, models)).toEqual({
      m1: { input_per_mtok: 1, output_per_mtok: 2 },
      m2: { input_per_mtok: 0, output_per_mtok: 0 },
      m3: { input_per_mtok: 0, output_per_mtok: 0 },
    })
  })
})

describe('optionsWithPrices', () => {
  it('writes prices_by_model as a JSON string and keeps every other option', () => {
    const options = optionsWithPrices(provider, { m1: { input_per_mtok: 1.5, output_per_mtok: 2 } })
    expect(options).toEqual({
      request_timeout: '5m',
      litellm_provider: 'zai',
      prices_by_model: '{"m1":{"input_per_mtok":1.5,"output_per_mtok":2}}',
    })
  })

  it('deletes the key when no prices are left', () => {
    const withKey = { ...provider, options: { ...provider.options, prices_by_model: '{"m1":{}}' } }
    expect(optionsWithPrices(withKey, {})).toEqual({ request_timeout: '5m', litellm_provider: 'zai' })
  })
})

describe('parsePrice', () => {
  it.each([
    ['1.25', { value: 1.25 }],
    [' 0 ', { value: 0 }],
    ['', { error: 'Enter a price.' }],
    ['abc', { error: 'Must be a number.' }],
    ['1e400', { error: 'Must be a finite number.' }],
    ['-1', { error: 'Cannot be negative.' }],
  ])('%j', (raw, want) => {
    expect(parsePrice(raw)).toEqual(want)
  })
})
