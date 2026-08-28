import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { EntityGraphData, MemoryItem } from '../../api/types'
import { GraphTab } from './GraphTab'

vi.mock('../../api/client', () => ({
  entityGraph: vi.fn(),
  entityMemories: vi.fn(),
  memoryChain: vi.fn(),
}))

// jsdom has no canvas: EChart inits a real chart on mount, which throws
// without a canvas 2d context. Stub the tree-shaken core entry point
// with a no-op instance that records registered handlers so tests can
// drive node/blank clicks directly — graphOption.test.ts carries the
// option-logic coverage, not the canvas render itself.
let clickHandler: ((params: unknown) => void) | null = null
let zrClickHandler: ((event: unknown) => void) | null = null

vi.mock('echarts/core', () => ({
  use: vi.fn(),
  init: vi.fn(() => ({
    setOption: vi.fn(),
    resize: vi.fn(),
    dispose: vi.fn(),
    on: vi.fn((_event: string, _opts: unknown, handler: (params: unknown) => void) => {
      clickHandler = handler
    }),
    getZr: vi.fn(() => ({
      on: vi.fn((_event: string, handler: (event: unknown) => void) => {
        zrClickHandler = handler
      }),
    })),
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

import { entityGraph, entityMemories } from '../../api/client'

const graph: EntityGraphData = {
  entities: [
    { id: 'e1', type: 'project', name: 'timothy', memory_count: 2 },
    { id: 'e2', type: 'person', name: 'sumon', memory_count: 1 },
  ],
  edges: [{ src: 'e1', dst: 'e2', weight: 1 }],
}

const memory: MemoryItem = {
  id: 'm1',
  type: 'semantic',
  content: 'Timothy is a self-hosted assistant.',
  status: 'active',
  confidence: 0.9,
  actor: 'agent',
  created_at: '2026-07-11T10:00:00Z',
}

function renderTab() {
  return render(
    <MemoryRouter>
      <GraphTab />
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  clickHandler = null
  zrClickHandler = null
  vi.mocked(entityGraph).mockResolvedValue(graph)
  vi.mocked(entityMemories).mockResolvedValue([memory])
})

describe('GraphTab', () => {
  it('renders the graph canvas with a legend chip per entity kind', async () => {
    renderTab()
    expect(await screen.findByTestId('entity-graph')).toBeInTheDocument()
    expect(screen.getByText('project')).toBeInTheDocument()
    expect(screen.getByText('person')).toBeInTheDocument()
  })

  it('clicking a node opens the detail panel with its memories', async () => {
    renderTab()
    await screen.findByTestId('entity-graph')
    clickHandler!({ dataType: 'node', data: { id: 'e1' } })
    const panel = await screen.findByTestId('entity-detail')
    expect(panel).toHaveTextContent('timothy')
    expect(await screen.findByTestId('entity-memory')).toHaveTextContent(
      'Timothy is a self-hosted assistant.',
    )
    expect(entityMemories).toHaveBeenCalledWith('e1')
  })

  it('clicking the empty canvas clears the selection', async () => {
    renderTab()
    await screen.findByTestId('entity-graph')
    clickHandler!({ dataType: 'node', data: { id: 'e1' } })
    await screen.findByTestId('entity-detail')
    zrClickHandler!({ target: null })
    await waitFor(() => expect(screen.queryByTestId('entity-detail')).toBeNull())
  })

  it('shows the empty state when no entities exist', async () => {
    vi.mocked(entityGraph).mockResolvedValue({ entities: [], edges: [] })
    renderTab()
    expect(await screen.findByText(/No entities yet/)).toBeInTheDocument()
  })

  it('survives an edge pointing at an unknown entity', async () => {
    vi.mocked(entityGraph).mockResolvedValue({
      entities: graph.entities,
      edges: [{ src: 'e1', dst: 'nope', weight: 3 }],
    })
    renderTab()
    expect(await screen.findByTestId('entity-graph')).toBeInTheDocument()
  })

  it('toggles a legend chip to hide its kind, struck through', async () => {
    renderTab()
    await screen.findByTestId('entity-graph')
    const chip = screen.getByText('project')
    expect(chip).not.toHaveStyle({ textDecoration: 'line-through' })
    fireEvent.click(chip)
    expect(chip).toHaveStyle({ textDecoration: 'line-through' })
    fireEvent.click(chip)
    expect(chip).not.toHaveStyle({ textDecoration: 'line-through' })
  })
})
