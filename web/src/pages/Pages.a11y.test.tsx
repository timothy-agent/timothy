import axe from 'axe-core'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { BudgetStatus, UsageSummary } from '../api/types'
import { Analytics } from './Analytics'

vi.mock('../api/client', () => ({
  catalogPrices: vi.fn(),
  usageBudget: vi.fn(),
  usageCache: vi.fn(),
  usageLatency: vi.fn(),
  usageSeries: vi.fn(),
  usageSummary: vi.fn(),
  usageTotals: vi.fn(),
  usageUnpriced: vi.fn(),
}))

// jsdom has no canvas: EChart inits a real chart on mount, which throws
// without a canvas 2d context. Stub the tree-shaken core entry point
// with a no-op instance, same as Analytics.test.tsx.
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

const axeOptions = { rules: { 'color-contrast': { enabled: false } } }

function expectSingleH1() {
  expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(usageSummary).mockResolvedValue([summary])
  vi.mocked(usageSeries).mockResolvedValue([])
  vi.mocked(usageTotals).mockResolvedValue([])
  vi.mocked(usageLatency).mockResolvedValue([])
  vi.mocked(usageCache).mockResolvedValue([])
  vi.mocked(usageBudget).mockResolvedValue(calmBudget)
  vi.mocked(usageUnpriced).mockResolvedValue([])
  vi.mocked(catalogPrices).mockResolvedValue([])
})

describe('Analytics', () => {
  it('has no axe violations and one h1', async () => {
    const { container } = renderPage()
    await screen.findByText('Spend today')
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })
})
