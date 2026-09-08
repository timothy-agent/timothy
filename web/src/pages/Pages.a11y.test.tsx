import axe from 'axe-core'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { MemoryItem } from '../api/types'
import { Memory } from './Memory'

// Sibling of Settings.a11y.test.tsx: axe over the pages left outside
// Settings' own suite. Each slice of the design-system migration adds
// its own describe block here with its own local mocks.

vi.mock('../api/client', () => ({
  listMemories: vi.fn(),
  addMemory: vi.fn(),
  resolveMemory: vi.fn(),
  memoryChain: vi.fn(),
  searchMemories: vi.fn(),
  entityGraph: vi.fn(),
  entityMemories: vi.fn(),
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

import { entityGraph, listMemories } from '../api/client'

const axeOptions = { rules: { 'color-contrast': { enabled: false } } }

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

function expectSingleH1() {
  expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listMemories).mockResolvedValue([pendingMemory])
  vi.mocked(entityGraph).mockResolvedValue({
    entities: [{ id: 'e1', type: 'project', name: 'timothy', memory_count: 1 }],
    edges: [],
  })
})

describe('Memory', () => {
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
