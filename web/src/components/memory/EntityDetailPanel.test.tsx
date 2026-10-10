import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { EntityNode, MemoryItem } from '../../api/types'
import { EntityDetailPanel } from './EntityDetailPanel'

vi.mock('../../api/client', () => ({
  entityMemories: vi.fn(),
  memoryChain: vi.fn(),
}))

import { entityMemories } from '../../api/client'

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

const entity: EntityNode = { id: 'e1', type: 'project', name: 'timothy', memory_count: 52 }

function page(n: number, from = 0): MemoryItem[] {
  return Array.from({ length: n }, (_, i) => ({
    id: `m${String(from + i).padStart(3, '0')}`,
    type: 'semantic',
    content: `entity fact ${from + i}`,
    status: 'active',
    confidence: 0.9,
    actor: 'agent',
    created_at: new Date(Date.UTC(2026, 9, 1, 12, 0) - (from + i) * 60_000).toISOString(),
  }))
}

afterEach(cleanup)
beforeEach(() => {
  observerCallbacks = []
  vi.stubGlobal('IntersectionObserver', FakeIntersectionObserver)
  vi.clearAllMocks()
})

describe('EntityDetailPanel', () => {
  it('loads more entity memories on scroll without duplicates', async () => {
    const first = page(50)
    const last = first[49]
    vi.mocked(entityMemories).mockImplementation((_id, cursor) =>
      Promise.resolve(cursor ? [last, ...page(2, 50)] : first),
    )
    render(<EntityDetailPanel entity={entity} />)
    await waitFor(() => expect(screen.getAllByTestId('entity-memory')).toHaveLength(50))
    expect(entityMemories).toHaveBeenCalledWith('e1')
    act(() => {
      observerCallbacks.at(-1)?.([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver)
    })
    await waitFor(() => expect(screen.getAllByTestId('entity-memory')).toHaveLength(52))
    expect(entityMemories).toHaveBeenLastCalledWith('e1', { before: last.created_at, beforeId: last.id })
    expect(screen.queryByTestId('entity-memories-sentinel')).toBeNull()
  })
})
