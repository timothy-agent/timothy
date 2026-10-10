import { act, cleanup, render, renderHook, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { MemoryItem } from '../api/types'

vi.mock('../api/client', () => ({
  countMemories: vi.fn(),
  listMemories: vi.fn(),
}))
vi.mock('sonner', () => ({ toast: { error: vi.fn() } }))

import type { SessionCursor } from '../api/client'
import { countMemories, listMemories } from '../api/client'
import { memoryPageSize, notifyMemoryChanged, useMemoryPages, usePendingMemories } from './memory'

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

// rows builds n memories newest first, starting at minute `from`.
function rows(prefix: string, n: number, from = 0): MemoryItem[] {
  return Array.from({ length: n }, (_, i) => ({
    id: `${prefix}${String(from + i).padStart(3, '0')}`,
    type: 'semantic',
    content: `${prefix} fact ${from + i}`,
    status: 'active',
    confidence: 0.9,
    actor: 'agent',
    created_at: new Date(Date.UTC(2026, 9, 1, 12, 0) - (from + i) * 60_000).toISOString(),
  }))
}

type Loader = (cursor?: SessionCursor) => Promise<MemoryItem[]>

let api: ReturnType<typeof useMemoryPages> | null = null
function Harness({ load }: { load: Loader }) {
  api = useMemoryPages(load, 'load failed')
  return (
    <div>
      {api.items.map((m) => (
        <p key={m.id} data-testid="row">
          {m.id}
        </p>
      ))}
      {api.hasMore && <div ref={api.sentinelRef} data-testid="sentinel" />}
    </div>
  )
}

const ids = () => screen.queryAllByTestId('row').map((el) => el.textContent)

afterEach(cleanup)
beforeEach(() => {
  observerCallbacks = []
  api = null
  vi.stubGlobal('IntersectionObserver', FakeIntersectionObserver)
  vi.clearAllMocks()
})

describe('usePendingMemories', () => {
  it('reads the count endpoint and never downloads the list', async () => {
    vi.mocked(countMemories).mockResolvedValue(12)
    const { result } = renderHook(() => usePendingMemories())
    await waitFor(() => expect(result.current).toBe(12))
    expect(countMemories).toHaveBeenCalledWith('pending')
    expect(listMemories).not.toHaveBeenCalled()
  })

  it('recounts when the Memory page acts', async () => {
    vi.mocked(countMemories).mockResolvedValue(3)
    const { result } = renderHook(() => usePendingMemories())
    await waitFor(() => expect(result.current).toBe(3))
    vi.mocked(countMemories).mockResolvedValue(2)
    act(() => notifyMemoryChanged())
    await waitFor(() => expect(result.current).toBe(2))
    expect(listMemories).not.toHaveBeenCalled()
  })
})

describe('useMemoryPages', () => {
  it('appends the next page from the last row without duplicates', async () => {
    const first = rows('a', memoryPageSize)
    const last = first[first.length - 1]
    const load = vi.fn<Loader>((cursor) =>
      Promise.resolve(cursor ? [last, ...rows('a', 2, memoryPageSize)] : first),
    )
    render(<Harness load={load} />)
    await waitFor(() => expect(ids()).toHaveLength(memoryPageSize))
    scrollToSentinel()
    await waitFor(() => expect(ids()).toHaveLength(memoryPageSize + 2))
    expect(load).toHaveBeenLastCalledWith({ before: last.created_at, beforeId: last.id })
    expect(new Set(ids()).size).toBe(memoryPageSize + 2)
    expect(screen.queryByTestId('sentinel')).toBeNull()
  })

  it('restarts at page 1 on a new load and drops the old one\'s late page', async () => {
    let releaseMore: (page: MemoryItem[]) => void = () => {}
    const loadA = vi.fn<Loader>((cursor) =>
      cursor ? new Promise((resolve) => (releaseMore = resolve)) : Promise.resolve(rows('a', memoryPageSize)),
    )
    const loadB = vi.fn<Loader>(() => Promise.resolve(rows('b', 2)))
    const { rerender } = render(<Harness load={loadA} />)
    await waitFor(() => expect(ids()).toHaveLength(memoryPageSize))
    scrollToSentinel()
    rerender(<Harness load={loadB} />)
    await waitFor(() => expect(ids()).toEqual(['b000', 'b001']))
    expect(loadB).toHaveBeenCalledWith()
    await act(async () => releaseMore(rows('a', 3, memoryPageSize)))
    expect(ids()).toEqual(['b000', 'b001'])
  })

  it('refresh refetches page 1 only and keeps older loaded pages', async () => {
    const first = rows('a', memoryPageSize)
    const second = rows('a', 5, memoryPageSize)
    const load = vi.fn<Loader>((cursor) => Promise.resolve(cursor ? second : first))
    render(<Harness load={load} />)
    await waitFor(() => expect(ids()).toHaveLength(memoryPageSize))
    scrollToSentinel()
    await waitFor(() => expect(ids()).toHaveLength(memoryPageSize + 5))

    load.mockClear()
    const fresh = rows('n', 1)
    load.mockImplementation(() => Promise.resolve([...fresh, ...first.slice(0, memoryPageSize - 1)]))
    act(() => api!.refresh())
    await waitFor(() => expect(ids()[0]).toBe('n000'))
    expect(load).toHaveBeenCalledTimes(1)
    expect(load).toHaveBeenCalledWith()
    // The row that fell off page 1 and the second page stay, once each.
    expect(ids()).toHaveLength(memoryPageSize + 6)
    expect(new Set(ids()).size).toBe(memoryPageSize + 6)
  })

  it('drop removes resolved rows locally', async () => {
    const load = vi.fn<Loader>(() => Promise.resolve(rows('a', 3)))
    render(<Harness load={load} />)
    await waitFor(() => expect(ids()).toHaveLength(3))
    act(() => api!.drop(['a001']))
    expect(ids()).toEqual(['a000', 'a002'])
  })
})
