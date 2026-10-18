import type { AdminProvider, AvailableModel } from '../../api/types'

// OperatorPrice is one options.prices_by_model entry (the gateway's
// router.ModelPrices), USD per million tokens. Keys this page does not
// edit, like cache_read_per_mtok, are kept as they are.
export type OperatorPrice = { input_per_mtok?: number; output_per_mtok?: number } & Record<string, unknown>
export type OperatorPrices = Record<string, OperatorPrice>

// parseOperatorPrices decodes options.prices_by_model, a JSON object
// stored as a string. Absent or undecodable is empty, as in the
// gateway's model list.
export function parseOperatorPrices(raw: string | undefined): OperatorPrices {
  if (!raw) return {}
  try {
    const v: unknown = JSON.parse(raw)
    return v && typeof v === 'object' && !Array.isArray(v) ? (v as OperatorPrices) : {}
  } catch {
    return {}
  }
}

// withPrice sets model's input and output price.
export function withPrice(prices: OperatorPrices, model: string, input: number, output: number): OperatorPrices {
  return { ...prices, [model]: { ...prices[model], input_per_mtok: input, output_per_mtok: output } }
}

// withoutPrice drops model's operator price.
export function withoutPrice(prices: OperatorPrices, model: string): OperatorPrices {
  const next = { ...prices }
  delete next[model]
  return next
}

// unpricedListed is the listed model ids that have no operator price.
export function unpricedListed(prices: OperatorPrices, models: AvailableModel[]): string[] {
  return models.filter((m) => !Object.hasOwn(prices, m.id)).map((m) => m.id)
}

// withListedFree gives every listed model without an operator price an
// explicit price of 0.
export function withListedFree(prices: OperatorPrices, models: AvailableModel[]): OperatorPrices {
  let next = prices
  for (const id of unpricedListed(prices, models)) next = withPrice(next, id, 0, 0)
  return next
}

// optionsWithPrices is provider's full options map with prices_by_model
// replaced; an empty map deletes the key. PATCH replaces options whole,
// so every other key must ride along.
export function optionsWithPrices(provider: AdminProvider, prices: OperatorPrices): NonNullable<AdminProvider['options']> {
  const options: NonNullable<AdminProvider['options']> = { ...provider.options }
  if (Object.keys(prices).length > 0) {
    options.prices_by_model = JSON.stringify(prices)
  } else {
    delete options.prices_by_model
  }
  return options
}

// parsePrice reads one price field: a finite, non-negative number, the
// same rule the admin API enforces. Empty is an error, never a guessed 0.
export function parsePrice(raw: string): { value: number } | { error: string } {
  const s = raw.trim()
  if (s === '') return { error: 'Enter a price.' }
  const v = Number(s)
  if (Number.isNaN(v)) return { error: 'Must be a number.' }
  if (!Number.isFinite(v)) return { error: 'Must be a finite number.' }
  if (v < 0) return { error: 'Cannot be negative.' }
  return { value: v }
}
