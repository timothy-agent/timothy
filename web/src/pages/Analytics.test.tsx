import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { BudgetStatus, GroupTotal, UsagePoint, UsageSummary } from '../api/types'
import { Analytics, sortGroupsByTotal } from './Analytics'
import * as echartsCore from 'echarts/core'

vi.mock('../api/client', () => ({
  catalogPrices: vi.fn(),
  usageBudget: vi.fn(),
  usageCache: vi.fn(),
  usageLatency: vi.fn(),
  usageSeries: vi.fn(),
  usageSessions: vi.fn(),
  usageSummary: vi.fn(),
  usageTotals: vi.fn(),
  usageUnpriced: vi.fn(),
}))

// jsdom has no canvas: EChart inits a real chart on mount, which throws
// without a canvas 2d context. Stub the tree-shaken core entry point
// with a no-op instance — the option builders (options.ts) are what
// carry chart-logic test coverage, not the canvas render itself.
vi.mock('echarts/core', () => ({
  use: vi.fn(),
  init: vi.fn(() => ({
    setOption: vi.fn(),
    resize: vi.fn(),
    dispose: vi.fn(),
    getZr: vi.fn(),
  })),
}))
vi.mock('echarts/charts', () => ({ BarChart: {}, LineChart: {}, PieChart: {}, GaugeChart: {}, GraphChart: {} }))
vi.mock('echarts/components', () => ({
  GridComponent: {},
  TooltipComponent: {},
  LegendComponent: {},
  TitleComponent: {},
  DataZoomComponent: {},
  MarkLineComponent: {},
}))
vi.mock('echarts/renderers', () => ({ CanvasRenderer: {} }))

import {
  catalogPrices,
  usageBudget,
  usageCache,
  usageLatency,
  usageSeries,
  usageSessions,
  usageSummary,
  usageTotals,
  usageUnpriced,
} from '../api/client'

const summary: UsageSummary = {
  currency: 'USD',
  cost: 2.5,
  unbilled_cost: 0,
  input_tokens: 1000,
  output_tokens: 500,
  cache_read_tokens: 0,
  cache_write_tokens: 0,
  requests: 10,
  errors: 0,
  unpriced_requests: 0,
  unpriced_input_tokens: 0,
  unpriced_output_tokens: 0,
}

const calmBudget: BudgetStatus = {
  day: { currency: 'USD', limit: { amount: 10, currency: 'USD' }, spend: 2.5, over: false },
  month: { currency: 'USD', limit: null, spend: 2.5, over: false },
}

function renderPage() {
  return render(
    <MemoryRouter>
      <Analytics />
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(usageSummary).mockResolvedValue([summary])
  vi.mocked(usageSeries).mockResolvedValue([])
  vi.mocked(usageTotals).mockResolvedValue([])
  vi.mocked(usageSessions).mockResolvedValue([])
  vi.mocked(usageLatency).mockResolvedValue([])
  vi.mocked(usageCache).mockResolvedValue([])
  vi.mocked(usageBudget).mockResolvedValue(calmBudget)
  vi.mocked(usageUnpriced).mockResolvedValue([])
  vi.mocked(catalogPrices).mockResolvedValue([])
})

describe('sortGroupsByTotal', () => {
  it('orders groups by summed value across rows, descending', () => {
    const rows = [
      { bucket: 'a', openai: 2, anthropic: 10, local: 1 },
      { bucket: 'b', openai: 4, anthropic: 0, local: 1 },
    ]
    // openai: 6, anthropic: 10, local: 2
    expect(sortGroupsByTotal(['openai', 'anthropic', 'local'], rows)).toEqual(['anthropic', 'openai', 'local'])
  })

  it('breaks ties alphabetically', () => {
    const rows = [{ bucket: 'a', zeta: 5, alpha: 5 }]
    expect(sortGroupsByTotal(['zeta', 'alpha'], rows)).toEqual(['alpha', 'zeta'])
  })

  it('treats a group missing from a row as zero', () => {
    const rows = [{ bucket: 'a', openai: 3 }]
    expect(sortGroupsByTotal(['openai', 'anthropic'], rows)).toEqual(['openai', 'anthropic'])
  })
})

describe('Analytics budget alert', () => {
  it('shows no banner while spend stays under budget', async () => {
    renderPage()
    await waitFor(() => expect(screen.getAllByText('$2.50').length).toBeGreaterThan(0))
    expect(screen.queryByRole('alert')).toBeNull()
    // The day budget hint only surfaces on the "Today" range.
    fireEvent.click(screen.getByText('Today'))
    await waitFor(() => expect(screen.getByText('of $10.00 budget')).toBeInTheDocument())
  })

  it('shows a banner naming every window over budget', async () => {
    vi.mocked(usageBudget).mockResolvedValue({
      day: { currency: 'USD', limit: { amount: 1, currency: 'USD' }, spend: 1.5, over: true },
      month: { currency: 'USD', limit: { amount: 100, currency: 'USD' }, spend: 120, over: true },
    })
    renderPage()
    const banner = await screen.findByRole('alert')
    expect(banner).toHaveTextContent('Daily budget reached: $1.50 spent of $1.00.')
    expect(banner).toHaveTextContent('Monthly budget reached: $120.00 spent of $100.00.')
  })

  it('stays silent when the budget endpoint fails', async () => {
    vi.mocked(usageBudget).mockRejectedValue(new Error('gateway down'))
    renderPage()
    // The shared widget-failure note appears; no budget banner.
    await screen.findByText(/widgets failed to load/)
    expect(screen.queryByRole('alert')).toBeNull()
  })
})

describe('Analytics spend tile', () => {
  it('defaults to "today" and labels the spend tile after the selected range', async () => {
    renderPage()
    await waitFor(() => expect(screen.getByText('Spend today')).toBeInTheDocument())

    fireEvent.click(screen.getByText('7 days'))
    await waitFor(() => expect(screen.getByText('Spend this week')).toBeInTheDocument())

    fireEvent.click(screen.getByText('30 days'))
    await waitFor(() => expect(screen.getByText('Spend this month')).toBeInTheDocument())
  })
})

describe('Analytics unbilled spend annotation', () => {
  it('shows the muted amount beside the spend tile, with an "unbilled" tooltip and no "notional" copy anywhere', async () => {
    vi.mocked(usageSummary).mockResolvedValue([{ ...summary, unbilled_cost: 0.42 }])
    renderPage()
    const annotation = await screen.findByText('+$0.4200')
    fireEvent.focus(annotation)
    expect(await screen.findByText('unbilled')).toBeInTheDocument()
    expect(screen.queryByText(/notional/i)).toBeNull()
  })

  it('converts the unbilled amount into the display currency when a stored fx rate exists', async () => {
    vi.mocked(usageSummary).mockResolvedValue([
      { ...summary, unbilled_cost: 0.42, converted_unbilled_cost: 0.39, converted_currency: 'EUR' },
    ])
    renderPage()
    expect(await screen.findByText('+€0.3900')).toBeInTheDocument()
  })

  it('falls back to the source-currency unbilled amount when no fx rate is available', async () => {
    vi.mocked(usageSummary).mockResolvedValue([{ ...summary, unbilled_cost: 0.42 }])
    renderPage()
    expect(await screen.findByText('+$0.4200')).toBeInTheDocument()
  })

  it('omits the annotation entirely when unbilled_cost is zero', async () => {
    renderPage() // default `summary` fixture carries unbilled_cost: 0
    await waitFor(() => expect(screen.getAllByText('$2.50').length).toBeGreaterThan(0))
    expect(screen.queryByText(/unbilled|notional/)).toBeNull()
  })
})

describe('Analytics converted spend display', () => {
  it('shows the converted amount as primary with the billed amount secondary', async () => {
    vi.mocked(usageSummary).mockResolvedValue([
      { ...summary, currency: 'USD', cost: 2.75, converted_amount: 2.53, converted_currency: 'EUR', rate_as_of: '2026-07-20' },
    ])
    renderPage()
    await waitFor(() => expect(screen.getByText('€2.53')).toBeInTheDocument())
    expect(screen.getByText('$2.75 billed')).toBeInTheDocument()
  })

  it('falls back to the billed amount alone when nothing converted', async () => {
    renderPage() // default `summary` fixture carries no converted_* fields
    await waitFor(() => expect(screen.getAllByText('$2.50').length).toBeGreaterThan(0))
    expect(screen.queryByText(/billed/)).toBeNull()
  })
})

describe('Analytics token tiles', () => {
  it('shows total input and output tokens for the range', async () => {
    vi.mocked(usageSummary).mockResolvedValue([
      {
        ...summary,
        input_tokens: 1_250_000,
        output_tokens: 84_000,
        cache_read_tokens: 400_000,
      },
    ])
    renderPage()
    expect(await screen.findByText('Input tokens')).toBeTruthy()
    expect(screen.getByText('1.3M')).toBeTruthy()
    expect(screen.getByText('Output tokens')).toBeTruthy()
    expect(screen.getByText('84.0k')).toBeTruthy()
    expect(screen.getByText('400.0k cached reads')).toBeTruthy()
  })
})

describe('Analytics unpriced usage', () => {
  it('notes unpriced calls with a catalog estimate', async () => {
    vi.mocked(usageSummary).mockResolvedValue([{ ...summary, unpriced_requests: 4 }])
    vi.mocked(usageUnpriced).mockResolvedValue([
      { provider: 'openai', model: 'gpt-5.6-sol', unpriced_input_tokens: 1_000_000, unpriced_output_tokens: 100_000 },
    ])
    // gpt-5.6-sol (openai) prices from the synced catalog: $5/mtok in, $30/mtok out.
    vi.mocked(catalogPrices).mockResolvedValue([
      { provider: 'openai', model: 'gpt-5.6-sol', price: { input_per_mtok: 5, output_per_mtok: 30 } },
    ])
    vi.mocked(usageSeries).mockResolvedValue([
      {
        bucket: '2026-07-24T00:00:00Z',
        group: 'gpt-5.6-sol',
        currency: 'USD',
        cost: 0,
        unbilled_cost: 0,
        input_tokens: 1_000_000,
        output_tokens: 100_000,
        requests: 4,
        errors: 0,
        unpriced_input_tokens: 1_000_000,
        unpriced_output_tokens: 100_000,
      },
    ])
    renderPage()
    // The note appears once usageSummary lands, but the estimate is
    // appended only after the chained catalogPrices fetch resolves —
    // await the estimate text itself, not the note.
    const note = await screen.findByText(/≈\$8\.00 at catalog prices/)
    expect(note).toHaveTextContent('had no configured price')
  })

  it('stays silent when every call is priced', async () => {
    renderPage()
    await waitFor(() => expect(screen.getAllByText('$2.50').length).toBeGreaterThan(0))
    expect(screen.queryByText(/had no configured price/)).toBeNull()
  })
})

const providerPoint: UsagePoint = {
  bucket: '2026-07-24T00:00:00Z',
  group: 'openai',
  currency: 'USD',
  cost: 1.5,
  unbilled_cost: 0,
  input_tokens: 100,
  output_tokens: 50,
  requests: 3,
  errors: 0,
  unpriced_input_tokens: 0,
  unpriced_output_tokens: 0,
}

const providerTotal: GroupTotal = {
  group: 'openai',
  currency: 'USD',
  cost: 1.5,
  input_tokens: 100,
  output_tokens: 50,
  requests: 3,
  unpriced_input_tokens: 0,
  unpriced_output_tokens: 0,
}

describe('Analytics chart legend selection', () => {
  const providerPointB: UsagePoint = { ...providerPoint, group: 'anthropic' }
  const providerTotalB: GroupTotal = { ...providerTotal, group: 'anthropic' }

  it('plain click isolates a legend entry, second click restores all', async () => {
    vi.mocked(usageSeries).mockImplementation(async (_from, _to, _bucket, group) =>
      group === 'provider' ? [providerPoint, providerPointB] : [],
    )
    vi.mocked(usageTotals).mockImplementation(async (_from, _to, group) =>
      group === 'provider' ? [providerTotal, providerTotalB] : [],
    )
    renderPage()

    const chart = (await screen.findByText('Spend by provider')).closest('section')
    if (!chart) throw new Error('chart section not found')
    // StatsLegend renders the series name as a plain text node with no
    // className, distinct from the "By provider"-style breakdown table
    // cell that wraps its text in a truncate span.
    const openaiEntry = (await within(chart).findAllByText('openai')).find((el) => el.className === '')
    const anthropicEntry = within(chart).getAllByText('anthropic').find((el) => el.className === '')
    if (!openaiEntry || !anthropicEntry) throw new Error('legend entry not found')

    expect(openaiEntry).not.toHaveStyle({ textDecoration: 'line-through' })
    fireEvent.click(openaiEntry)
    // Isolating openai hides anthropic but leaves openai visible.
    await waitFor(() => expect(anthropicEntry).toHaveStyle({ textDecoration: 'line-through' }))
    expect(openaiEntry).not.toHaveStyle({ textDecoration: 'line-through' })

    // Clicking the already-isolated entry again restores all.
    fireEvent.click(openaiEntry)
    await waitFor(() => expect(anthropicEntry).not.toHaveStyle({ textDecoration: 'line-through' }))
  })

  it('ctrl-click toggles just that entry, independent of other legends', async () => {
    renderPage()
    const chart = (await screen.findByText('Token consumption')).closest('section')
    if (!chart) throw new Error('chart section not found')
    const inputEntry = (await within(chart).findAllByText('input')).find((el) => el.className === '')
    if (!inputEntry) throw new Error('legend entry not found')

    fireEvent.click(inputEntry, { ctrlKey: true })
    await waitFor(() => expect(inputEntry).toHaveStyle({ textDecoration: 'line-through' }))
    // The sibling "output" entry in the same legend is untouched.
    const outputEntry = within(chart)
      .getAllByText('output')
      .find((el) => el.className === '')
    expect(outputEntry).not.toHaveStyle({ textDecoration: 'line-through' })

    // Ctrl-click again restores just that entry.
    fireEvent.click(inputEntry, { ctrlKey: true })
    await waitFor(() => expect(inputEntry).not.toHaveStyle({ textDecoration: 'line-through' }))
  })
})

// findChartOptionGetter locates the EChart init() instance whose
// container lives inside the given section, and returns a getter for
// its most recently applied option — several charts render on the
// page, each with its own init() call/instance.
async function findChartOptionGetter(section: HTMLElement) {
  const chartDiv = section.querySelector('.mt-3 > div')
  if (!chartDiv) throw new Error('chart container not found')
  const initMock = vi.mocked(echartsCore.init)
  await waitFor(() => expect(initMock.mock.calls.some((c) => c[0] === chartDiv)).toBe(true))
  const callIndex = initMock.mock.calls.findIndex((c) => c[0] === chartDiv)
  const instance = initMock.mock.results[callIndex].value
  return () => vi.mocked(instance.setOption).mock.calls.at(-1)?.[0]
}

describe('Analytics bars/lines view toggle', () => {
  it('defaults to lines and switches the spend-over-time chart to bars on click', async () => {
    vi.mocked(usageSeries).mockImplementation(async (_from, _to, _bucket, group) =>
      group === 'provider' ? [providerPoint] : [],
    )
    vi.mocked(usageTotals).mockImplementation(async (_from, _to, group) =>
      group === 'provider' ? [providerTotal] : [],
    )
    renderPage()
    const chart = (await screen.findByText('Spend by provider')).closest('section')
    if (!chart) throw new Error('chart section not found')
    const lastOption = await findChartOptionGetter(chart)
    await waitFor(() => expect((lastOption().series as Array<{ type: string }>)[0]?.type).toBe('line'))

    fireEvent.click(within(chart).getByText('Bars'))
    await waitFor(() => expect((lastOption().series as Array<{ type: string }>)[0]?.type).toBe('bar'))
  })

  it('defaults to lines and switches the tokens-in/out chart to bars on click', async () => {
    renderPage()
    const chart = (await screen.findByText('Token consumption')).closest('section')
    if (!chart) throw new Error('chart section not found')
    const lastOption = await findChartOptionGetter(chart)
    await waitFor(() => expect((lastOption().series as Array<{ type: string }>)[0]?.type).toBe('line'))

    fireEvent.click(within(chart).getByText('Bars'))
    await waitFor(() => expect((lastOption().series as Array<{ type: string }>)[0]?.type).toBe('bar'))
  })

  it('defaults to lines and switches the tokens-per-model chart to bars on click', async () => {
    vi.mocked(usageSeries).mockImplementation(async (_from, _to, _bucket, group) =>
      group === 'model' ? [{ ...providerPoint, group: 'gpt-5.6-sol' }] : [],
    )
    renderPage()
    const chart = (await screen.findByText('Tokens per model')).closest('section')
    if (!chart) throw new Error('chart section not found')
    const lastOption = await findChartOptionGetter(chart)
    await waitFor(() => expect((lastOption().series as Array<{ type: string }>)[0]?.type).toBe('line'))

    fireEvent.click(within(chart).getByText('Bars'))
    await waitFor(() => expect((lastOption().series as Array<{ type: string }>)[0]?.type).toBe('bar'))
  })

  it('has no toggle on the requests & errors panel', async () => {
    renderPage()
    const chart = (await screen.findByText('Requests & errors over time')).closest('section')
    if (!chart) throw new Error('chart section not found')
    expect(within(chart).queryByText('Bars')).toBeNull()
    expect(within(chart).queryByText('Lines')).toBeNull()
  })
})

describe('Analytics panel layout', () => {
  it('orders chart panels: spend by provider, spend by model, tokens per model, token consumption, latency/spend share, requests & errors', async () => {
    renderPage()
    const headings = (await screen.findAllByRole('heading', { level: 2 })).map((h) => h.textContent)
    const chartHeadings = [
      'Spend by provider',
      'Spend by model',
      'Tokens per model',
      'Token consumption',
      'Latency per provider',
      'Spend share by provider',
      'Requests & errors over time',
    ]
    const positions = chartHeadings.map((h) => headings.indexOf(h))
    expect(positions.every((p) => p >= 0)).toBe(true)
    expect(positions).toEqual([...positions].sort((a, b) => a - b))
    // Requests & errors is the last chart panel, ahead of the budget/table sections.
    expect(headings.indexOf('Requests & errors over time')).toBeGreaterThan(headings.indexOf('Latency per provider'))
  })
})

describe('Analytics zero-cost exclusion', () => {
  const freeModelPoint: UsagePoint = {
    bucket: '2026-07-24T00:00:00Z',
    group: 'local-llama',
    currency: 'USD',
    cost: 0,
    unbilled_cost: 0,
    input_tokens: 5_000,
    output_tokens: 2_000,
    requests: 2,
    errors: 0,
    unpriced_input_tokens: 0,
    unpriced_output_tokens: 0,
  }
  const freeModelTotal: GroupTotal = {
    group: 'local-llama',
    currency: 'USD',
    cost: 0,
    input_tokens: 5_000,
    output_tokens: 2_000,
    requests: 2,
    unpriced_input_tokens: 0,
    unpriced_output_tokens: 0,
  }
  const pricedModelPoint: UsagePoint = { ...providerPoint, group: 'gpt-5.6-sol' }
  const pricedModelTotal: GroupTotal = { ...providerTotal, group: 'gpt-5.6-sol' }

  it('excludes a zero-cost provider from the cost table but keeps it in token views', async () => {
    vi.mocked(usageTotals).mockImplementation(async (_from, _to, group) =>
      group === 'provider' ? [freeModelTotal, providerTotal] : [],
    )
    vi.mocked(usageSeries).mockImplementation(async (_from, _to, _bucket, group) =>
      group === 'model' ? [freeModelPoint, pricedModelPoint] : [],
    )
    renderPage()

    const providerTable = (await screen.findByText('Provider cost breakdown')).closest('section')
    if (!providerTable) throw new Error('provider cost table not found')
    expect(await within(providerTable).findByText('openai')).toBeInTheDocument()
    expect(within(providerTable).queryByText('local-llama')).toBeNull()

    // Same model, token-consumption chart: the free model's volume is
    // exactly the signal this chart exists to show, so it must render.
    const tokenChart = (await screen.findByText('Tokens per model')).closest('section')
    if (!tokenChart) throw new Error('token chart section not found')
    expect(await within(tokenChart).findByText('local-llama')).toBeInTheDocument()
  })

  it('excludes a zero-cost model from the cost-by-model chart', async () => {
    vi.mocked(usageTotals).mockImplementation(async (_from, _to, group) =>
      group === 'model' ? [freeModelTotal, pricedModelTotal] : [],
    )
    vi.mocked(usageSeries).mockImplementation(async (_from, _to, _bucket, group) =>
      group === 'model' ? [freeModelPoint, pricedModelPoint] : [],
    )
    renderPage()

    const modelCostChart = (await screen.findByText('Spend by model')).closest('section')
    if (!modelCostChart) throw new Error('model cost chart section not found')
    expect(await within(modelCostChart).findByText('gpt-5.6-sol')).toBeInTheDocument()
    expect(within(modelCostChart).queryByText('local-llama')).toBeNull()
  })
})
