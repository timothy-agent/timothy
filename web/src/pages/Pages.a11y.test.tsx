import axe from 'axe-core'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { BudgetStatus, MemoryItem, UsageSummary } from '../api/types'

// Sibling of Settings.a11y.test.tsx: axe on Home, Analytics, Memory and
// Knowledge, zero violations and one h1 each. vi.mock for '../api/client'
// is hoisted per module path, so every export any page needs is declared
// once here; each describe's beforeEach sets only the resolved values it
// cares about.
vi.mock('../api/client', () => ({
  listAgents: vi.fn(),
  listRoutes: vi.fn(),
  getSettings: vi.fn(),
  listMemories: vi.fn(),
  addMemory: vi.fn(),
  resolveMemory: vi.fn(),
  memoryChain: vi.fn(),
  searchMemories: vi.fn(),
  entityGraph: vi.fn(),
  entityMemories: vi.fn(),
  listKbCollections: vi.fn(),
  getKbCollection: vi.fn(),
  createKbCollection: vi.fn(),
  deleteKbCollection: vi.fn(),
  updateKbCollection: vi.fn(),
  listKbDocuments: vi.fn(),
  uploadKbDocument: vi.fn(),
  deleteKbDocument: vi.fn(),
  reingestKbDocument: vi.fn(),
  addKbDocumentFromUrl: vi.fn(),
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
// with a no-op instance.
vi.mock('echarts/core', () => ({
  use: vi.fn(),
  init: vi.fn(() => ({
    setOption: vi.fn(),
    resize: vi.fn(),
    dispose: vi.fn(),
    dispatchAction: vi.fn(),
    on: vi.fn(),
    getZr: vi.fn(() => ({ on: vi.fn() })),
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
  entityGraph,
  getSettings,
  listAgents,
  listKbCollections,
  listMemories,
  listRoutes,
  usageBudget,
  usageCache,
  usageLatency,
  usageSeries,
  usageSummary,
  usageTotals,
  usageUnpriced,
} from '../api/client'
import { Analytics } from './Analytics'
import { Home } from './Home'
import { Knowledge } from './Knowledge'
import { Memory } from './Memory'

const axeOptions = { rules: { 'color-contrast': { enabled: false } } }
// The Composer's hidden file input has no label, pre-existing and
// outside this migration's scope (same exemption as Chat.a11y.test.tsx).
const homeAxeOptions = { rules: { 'color-contrast': { enabled: false }, label: { enabled: false } } }

function expectSingleH1() {
  expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getSettings).mockResolvedValue({ settings: { transcribe_enabled: false }, values: {} })
})

describe('Home', () => {
  beforeEach(() => {
    vi.mocked(listAgents).mockResolvedValue([
      {
        id: 'a1',
        name: 'general',
        description: 'Everyday chat',
        prompt_overlay: '',
        route: '',
        skills: [],
        tools: [],
        memory: true,
        is_default: true,
        enabled: true,
      },
    ])
    vi.mocked(listMemories).mockResolvedValue([])
    vi.mocked(listRoutes).mockRejectedValue(new Error('not used on this page'))
    vi.mocked(listKbCollections).mockResolvedValue([])
  })

  it('has no axe violations', async () => {
    const { container } = render(
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route path="/" element={<Home />} />
        </Routes>
      </MemoryRouter>,
    )

    await screen.findByRole('button', { name: /general/ })
    expectSingleH1()
    const results = await axe.run(container, homeAxeOptions)
    expect(results.violations).toEqual([])
  })
})

describe('Analytics', () => {
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

  beforeEach(() => {
    vi.mocked(usageSummary).mockResolvedValue([summary])
    vi.mocked(usageSeries).mockResolvedValue([])
    vi.mocked(usageTotals).mockResolvedValue([])
    vi.mocked(usageLatency).mockResolvedValue([])
    vi.mocked(usageCache).mockResolvedValue([])
    vi.mocked(usageBudget).mockResolvedValue(calmBudget)
    vi.mocked(usageUnpriced).mockResolvedValue([])
    vi.mocked(catalogPrices).mockResolvedValue([])
  })

  it('has no axe violations and one h1', async () => {
    const { container } = render(
      <MemoryRouter>
        <Analytics />
      </MemoryRouter>,
    )
    await screen.findByText('Spend today')
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })
})

describe('Memory', () => {
  const pendingMemory: MemoryItem = {
    id: 'm1',
    type: 'semantic',
    content: 'User prefers aisle seats.',
    status: 'pending',
    confidence: 0.7,
    actor: 'agent',
    source_session: '11111111-1111-1111-1111-111111111111',
    created_at: '2026-07-11T10:00:00Z',
  }

  function renderMemory() {
    return render(
      <MemoryRouter initialEntries={['/memory']}>
        <Routes>
          <Route path="/memory/*" element={<Memory />} />
        </Routes>
      </MemoryRouter>,
    )
  }

  beforeEach(() => {
    vi.mocked(listMemories).mockResolvedValue([pendingMemory])
    vi.mocked(entityGraph).mockResolvedValue({
      entities: [{ id: 'e1', type: 'project', name: 'timothy', memory_count: 1 }],
      edges: [],
    })
  })

  it('has no axe violations on the queue tab', async () => {
    const { container } = renderMemory()
    await screen.findByTestId('queue-card')
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })

  it('has no axe violations on the browser tab', async () => {
    const { container } = renderMemory()
    await screen.findByTestId('queue-card')
    fireEvent.click(screen.getByRole('radio', { name: 'Browser' }))
    await screen.findByTestId('memory-search')
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })

  it('has no axe violations on the graph tab', async () => {
    const { container } = renderMemory()
    await screen.findByTestId('queue-card')
    fireEvent.click(screen.getByRole('radio', { name: 'Graph' }))
    await screen.findByTestId('entity-graph')
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })
})

describe('Knowledge', () => {
  beforeEach(() => {
    vi.mocked(listKbCollections).mockResolvedValue([
      {
        id: 'c1',
        name: 'product-docs',
        description: 'Product documentation for support agents.',
        doc_count: 2,
        chunk_count: 40,
        failed_count: 0,
        retrieval_weight: 1.0,
        created_at: '2026-08-01T00:00:00Z',
        updated_at: '2026-08-10T00:00:00Z',
      },
    ])
  })

  it('has no axe violations on the collections list', async () => {
    const { container } = render(
      <MemoryRouter initialEntries={['/knowledge']}>
        <Routes>
          <Route path="/knowledge/*" element={<Knowledge />} />
        </Routes>
      </MemoryRouter>,
    )

    await screen.findByText('product-docs')
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })
})
