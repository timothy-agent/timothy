import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { MemoryItem } from '../api/types'
import { Memory, KnowledgeRedirect } from './Memory'

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

import { entityGraph, listMemories, memoryChain, resolveMemory, searchMemories } from '../api/client'

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

function renderPage(initialEntry = '/memory') {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route path="/memory/*" element={<Memory />} />
      </Routes>
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listMemories).mockResolvedValue([pendingMemory])
  vi.mocked(resolveMemory).mockResolvedValue(undefined)
  vi.mocked(searchMemories).mockResolvedValue([])
})

describe('Memory queue', () => {
  it('renders pending cards with type, confidence, and source link', async () => {
    renderPage()
    const card = await screen.findByTestId('queue-card')
    expect(card).toHaveTextContent('User prefers aisle seats.')
    expect(card).toHaveTextContent('semantic')
    expect(card).toHaveTextContent('confidence 70%')
    expect(screen.getByRole('link', { name: /source session/ })).toHaveAttribute(
      'href',
      '/sessions/11111111-1111-1111-1111-111111111111',
    )
  })

  it('confirm resolves the card', async () => {
    renderPage()
    await screen.findByTestId('queue-card')
    fireEvent.click(screen.getByRole('button', { name: 'Confirm' }))
    await waitFor(() => expect(resolveMemory).toHaveBeenCalledWith('m1', 'confirm', undefined))
  })

  it('reject resolves the card', async () => {
    renderPage()
    await screen.findByTestId('queue-card')
    fireEvent.click(screen.getByRole('button', { name: 'Reject' }))
    await waitFor(() => expect(resolveMemory).toHaveBeenCalledWith('m1', 'reject', undefined))
  })

  it('edit-then-confirm sends the corrected content', async () => {
    renderPage()
    await screen.findByTestId('queue-card')
    fireEvent.click(screen.getByRole('button', { name: 'Edit' }))
    fireEvent.change(screen.getByTestId('edit-content'), {
      target: { value: 'User prefers window seats.' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save & confirm' }))
    await waitFor(() =>
      expect(resolveMemory).toHaveBeenCalledWith('m1', 'confirm', 'User prefers window seats.'),
    )
  })

  it('bulk confirm resolves every pending card', async () => {
    vi.mocked(listMemories).mockResolvedValue([
      pendingMemory,
      { ...pendingMemory, id: 'm2', content: 'Second fact.' },
    ])
    renderPage()
    await screen.findAllByTestId('queue-card')
    fireEvent.click(screen.getByRole('button', { name: 'Confirm all' }))
    await waitFor(() => expect(resolveMemory).toHaveBeenCalledTimes(2))
  })

  it('shows the empty state when nothing is pending', async () => {
    vi.mocked(listMemories).mockResolvedValue([])
    renderPage()
    expect(await screen.findByText(/Queue is empty/)).toBeInTheDocument()
  })
})

describe('Memory tabs', () => {
  it('defaults to the queue tab, with queue listed first', async () => {
    renderPage()
    await screen.findByTestId('queue-card')
    expect(screen.getByRole('radiogroup', { name: 'Memory view' })).toBeInTheDocument()
    const tabs = screen.getAllByRole('radio')
    expect(tabs.map((t) => t.textContent)).toEqual(['Queue', 'Browser', 'Graph'])
    expect(screen.getByRole('radio', { name: 'Queue' })).toHaveAttribute('data-state', 'on')
  })
})

describe('KnowledgeRedirect', () => {
  function renderRedirect(entry: string) {
    return render(
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path="/memory/knowledge/*" element={<KnowledgeRedirect />} />
          <Route path="/knowledge/*" element={<div data-testid="knowledge-page" />} />
        </Routes>
      </MemoryRouter>,
    )
  }

  it('forwards a knowledge deep link to the new top-level route', async () => {
    renderRedirect('/memory/knowledge/c1')
    expect(await screen.findByTestId('knowledge-page')).toBeInTheDocument()
  })
})

describe('Memory graph tab', () => {
  it('renders the entity graph', async () => {
    vi.mocked(entityGraph).mockResolvedValue({
      entities: [{ id: 'e1', type: 'project', name: 'timothy', memory_count: 1 }],
      edges: [],
    })
    renderPage()
    fireEvent.click(await screen.findByRole('radio', { name: 'Graph' }))
    expect(await screen.findByTestId('entity-graph')).toBeInTheDocument()
    expect(screen.getByText('project')).toBeInTheDocument()
  })
})

describe('Memory browser', () => {
  it('searches through the retrieval endpoint', async () => {
    vi.mocked(searchMemories).mockResolvedValue([
      { id: 'r1', type: 'semantic', content: 'User lives in Porto.', score: 0.02 },
    ])
    renderPage()
    fireEvent.click(await screen.findByRole('radio', { name: 'Browser' }))
    fireEvent.change(screen.getByTestId('memory-search'), {
      target: { value: 'where does the user live' },
    })
    fireEvent.keyDown(screen.getByTestId('memory-search'), { key: 'Enter' })
    await waitFor(() => expect(searchMemories).toHaveBeenCalledWith('where does the user live'))
    expect(await screen.findByTestId('search-results')).toHaveTextContent('User lives in Porto.')
  })

  it('opens the supersede history dialog and lists the chain oldest first', async () => {
    vi.mocked(searchMemories).mockResolvedValue([
      { id: 'r1', type: 'semantic', content: 'User lives in Porto.', score: 0.02 },
    ])
    vi.mocked(memoryChain).mockResolvedValue([
      { ...pendingMemory, id: 'v1', content: 'User lives in Lisbon.', status: 'archived' },
      { ...pendingMemory, id: 'v2', content: 'User lives in Porto.', status: 'active' },
    ])
    renderPage()
    fireEvent.click(await screen.findByRole('radio', { name: 'Browser' }))
    fireEvent.change(screen.getByTestId('memory-search'), {
      target: { value: 'where does the user live' },
    })
    fireEvent.keyDown(screen.getByTestId('memory-search'), { key: 'Enter' })
    const searchResults = await screen.findByTestId('search-results')
    fireEvent.click(within(searchResults).getByRole('button', { name: 'history' }))

    await waitFor(() => expect(memoryChain).toHaveBeenCalledWith('r1'))
    const chainList = await screen.findByTestId('chain-list')
    expect(chainList).toHaveTextContent('User lives in Lisbon.')
    expect(chainList).toHaveTextContent('User lives in Porto.')
    const items = within(chainList).getAllByRole('listitem')
    expect(items[0]).toHaveTextContent('v1')
    expect(items[0]).toHaveTextContent('User lives in Lisbon.')
    expect(items[1]).toHaveTextContent('v2')
    expect(items[1]).toHaveTextContent('User lives in Porto.')
  })
})
