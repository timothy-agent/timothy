import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { MemoryItem } from '../api/types'
import { Memory, KnowledgeRedirect } from './Memory'

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

vi.mock('../onboarding/context', async () => {
  const { onboardingState } = await import('../onboarding/testing')
  return { useOnboarding: () => onboardingState() }
})

vi.mock('../api/client', () => ({
  getToken: vi.fn(() => ''),
  listMemories: vi.fn(),
  countMemories: vi.fn().mockResolvedValue(0),
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

import {
  addMemory,
  countMemories,
  entityGraph,
  listMemories,
  memoryChain,
  resolveMemory,
  searchMemories,
} from '../api/client'
import { toast } from 'sonner'

// jsdom has no IntersectionObserver: keep each observer's callback so a
// test can report the sentinel as visible.
let observerCallbacks: IntersectionObserverCallback[] = []
class FakeIntersectionObserver {
  constructor(cb: IntersectionObserverCallback) {
    observerCallbacks.push(cb)
  }
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return []
  }
}

function scrollToSentinel() {
  const cb = observerCallbacks.at(-1)
  act(() => {
    cb?.([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver)
  })
}

// page builds n memories newest first, starting at minute `from`.
function page(status: MemoryItem['status'], n: number, from = 0): MemoryItem[] {
  return Array.from({ length: n }, (_, i) => ({
    id: `${status}-${String(from + i).padStart(3, '0')}`,
    type: 'semantic',
    content: `${status} fact ${from + i}`,
    status,
    confidence: 0.9,
    actor: 'agent',
    created_at: new Date(Date.UTC(2026, 9, 1, 12, 0) - (from + i) * 60_000).toISOString(),
  }))
}

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
  observerCallbacks = []
  vi.stubGlobal('IntersectionObserver', FakeIntersectionObserver)
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

  it('shows prior and proposed text side by side for a correction', async () => {
    vi.mocked(listMemories).mockResolvedValue([
      {
        ...pendingMemory,
        content: 'User lives in Berlin.',
        supersedes: { id: 'old', content: 'User lives in Amsterdam.' },
      },
    ])
    renderPage()
    const comparison = await screen.findByTestId('supersede-comparison')
    expect(within(comparison).getByText('Existing fact')).toBeInTheDocument()
    expect(within(comparison).getByText('User lives in Amsterdam.')).toBeInTheDocument()
    expect(within(comparison).getByText('Proposed correction')).toBeInTheDocument()
    expect(within(comparison).getByText('User lives in Berlin.')).toBeInTheDocument()
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

  it('bulk confirm pages through and resolves every pending memory', async () => {
    const first = page('pending', 50)
    const last = first[49]
    vi.mocked(listMemories).mockImplementation((_status, opts) =>
      Promise.resolve(opts?.cursor ? page('pending', 10, 50) : first),
    )
    renderPage()
    await waitFor(() => expect(screen.getAllByTestId('queue-card')).toHaveLength(50))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm all' }))
    await waitFor(() => expect(resolveMemory).toHaveBeenCalledTimes(60))
    expect(listMemories).toHaveBeenCalledWith('pending', {
      cursor: { before: last.created_at, beforeId: last.id },
    })
    const resolvedIds = vi.mocked(resolveMemory).mock.calls.map(([id, action]) => {
      expect(action).toBe('confirm')
      return id
    })
    expect(new Set(resolvedIds).size).toBe(60)
    expect(resolvedIds).toContain('pending-059')
  })

  it('shows the empty state when nothing is pending', async () => {
    vi.mocked(listMemories).mockResolvedValue([])
    renderPage()
    expect(await screen.findByText(/Queue is empty/)).toBeInTheDocument()
  })

  it('loads the next page on scroll without duplicates and shows the server count', async () => {
    const first = page('pending', 50)
    const last = first[49]
    vi.mocked(countMemories).mockResolvedValue(52)
    vi.mocked(listMemories).mockImplementation((_status, opts) =>
      Promise.resolve(opts?.cursor ? [last, ...page('pending', 2, 50)] : first),
    )
    renderPage()
    await waitFor(() => expect(screen.getAllByTestId('queue-card')).toHaveLength(50))
    expect(await screen.findByText('52 pending')).toBeInTheDocument()
    scrollToSentinel()
    await waitFor(() => expect(screen.getAllByTestId('queue-card')).toHaveLength(52))
    expect(listMemories).toHaveBeenLastCalledWith('pending', {
      cursor: { before: last.created_at, beforeId: last.id },
    })
    expect(screen.queryByTestId('queue-sentinel')).toBeNull()
  })

  it('resolving a card refetches page 1 only and keeps loaded older pages', async () => {
    const first = page('pending', 50)
    vi.mocked(listMemories).mockImplementation((_status, opts) =>
      Promise.resolve(opts?.cursor ? page('pending', 3, 50) : first),
    )
    renderPage()
    await waitFor(() => expect(screen.getAllByTestId('queue-card')).toHaveLength(50))
    scrollToSentinel()
    await waitFor(() => expect(screen.getAllByTestId('queue-card')).toHaveLength(53))

    vi.mocked(listMemories).mockClear()
    // Page 1 after the resolve: the next row moves up into it.
    vi.mocked(listMemories).mockResolvedValue([...first.slice(1), ...page('pending', 1, 50)])
    fireEvent.click(screen.getAllByRole('button', { name: 'Confirm' })[0])
    await waitFor(() => expect(resolveMemory).toHaveBeenCalledWith('pending-000', 'confirm', undefined))
    await waitFor(() => expect(screen.getAllByTestId('queue-card')).toHaveLength(52))
    expect(listMemories).toHaveBeenCalledTimes(1)
    expect(listMemories).toHaveBeenCalledWith('pending', { cursor: undefined })
    expect(screen.queryByText('pending fact 0')).toBeNull()
    expect(screen.getByText('pending fact 52')).toBeInTheDocument()
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
  it('loads more active memories on scroll without duplicates', async () => {
    const first = page('active', 50)
    const last = first[49]
    vi.mocked(listMemories).mockImplementation((status, opts) => {
      if (status === 'pending') return Promise.resolve([])
      return Promise.resolve(opts?.cursor ? [last, ...page('active', 4, 50)] : first)
    })
    renderPage()
    fireEvent.click(await screen.findByRole('radio', { name: 'Browser' }))
    await screen.findByText('active fact 49')
    scrollToSentinel()
    await screen.findByText('active fact 53')
    expect(listMemories).toHaveBeenLastCalledWith('active', {
      cursor: { before: last.created_at, beforeId: last.id },
    })
    expect(screen.getAllByText(/^active fact /)).toHaveLength(54)
    expect(screen.queryByTestId('browse-sentinel')).toBeNull()
  })

  it('shows when a manual add needs review', async () => {
    vi.mocked(addMemory).mockResolvedValue({ id: 'm2', status: 'pending' })
    renderPage()
    fireEvent.click(await screen.findByRole('radio', { name: 'Browser' }))
    fireEvent.change(screen.getByTestId('manual-add'), { target: { value: 'User prefers dark mode.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => {
      expect(addMemory).toHaveBeenCalledWith('User prefers dark mode.', 'semantic')
      expect(toast.info).toHaveBeenCalledWith('Memory added to the review queue')
    })
  })

  it('shows when a manual add matches a rejected fact', async () => {
    vi.mocked(addMemory).mockResolvedValue({ id: '', status: 'dropped' })
    renderPage()
    fireEvent.click(await screen.findByRole('radio', { name: 'Browser' }))
    fireEvent.change(screen.getByTestId('manual-add'), { target: { value: 'User lives in Porto.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => {
      expect(toast.info).toHaveBeenCalledWith('Memory was not added because it matches a rejected fact')
    })
  })

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
