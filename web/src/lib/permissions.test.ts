import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { PendingPermission } from '../api/types'

vi.mock('../api/client', () => ({
  listPendingPermissions: vi.fn(),
}))
vi.mock('./events', () => ({
  subscribeEvents: vi.fn(),
}))

import { listPendingPermissions } from '../api/client'
import { subscribeEvents } from './events'
import { newlySeen, permissionHref, toastSessionLabel, usePendingPermissions } from './permissions'

const pending: PendingPermission = {
  id: 'p1',
  session_id: 's1',
  session_title: 'Gmail cleanup',
  mission_id: '',
  tool: 'gmail_search',
  args: { query: 'is:unread' },
  danger: 'safe',
  rationale: 'read the inbox',
  origin_kind: 'chat',
  requested_at: '2026-08-01T10:00:00Z',
}

afterEach(() => {
  vi.clearAllTimers()
  vi.useRealTimers()
})

beforeEach(() => {
  vi.clearAllMocks()
})

describe('usePendingPermissions', () => {
  it('fetches the pending list on mount', async () => {
    vi.mocked(listPendingPermissions).mockResolvedValue([pending])
    vi.mocked(subscribeEvents).mockReturnValue(() => {})

    const { result } = renderHook(() => usePendingPermissions())

    await waitFor(() => expect(result.current).toEqual([pending]))
  })

  it('refetches when a "permission" signal arrives', async () => {
    vi.mocked(listPendingPermissions)
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([pending])
    let onSignal: ((s: { kind: string; id: string }) => void) | undefined
    vi.mocked(subscribeEvents).mockImplementation((cb) => {
      onSignal = cb
      return () => {}
    })

    const { result } = renderHook(() => usePendingPermissions())
    await waitFor(() => expect(result.current).toEqual([]))

    act(() => {
      onSignal?.({ kind: 'permission', id: 's1' })
    })

    await waitFor(() => expect(result.current).toEqual([pending]))
    expect(listPendingPermissions).toHaveBeenCalledTimes(2)
  })

  it('ignores a signal whose kind is not "permission"', async () => {
    vi.mocked(listPendingPermissions).mockResolvedValue([])
    let onSignal: ((s: { kind: string; id: string }) => void) | undefined
    vi.mocked(subscribeEvents).mockImplementation((cb) => {
      onSignal = cb
      return () => {}
    })

    renderHook(() => usePendingPermissions())
    await waitFor(() => expect(listPendingPermissions).toHaveBeenCalledTimes(1))

    act(() => {
      onSignal?.({ kind: 'session', id: 's1' })
      onSignal?.({ kind: 'mission', id: 'm1' })
    })

    // Neither signal is 'permission', so no extra refetch was triggered.
    expect(listPendingPermissions).toHaveBeenCalledTimes(1)
  })

  it('unsubscribes from events on unmount', () => {
    vi.mocked(listPendingPermissions).mockResolvedValue([])
    const unsubscribe = vi.fn()
    vi.mocked(subscribeEvents).mockReturnValue(unsubscribe)

    const { unmount } = renderHook(() => usePendingPermissions())
    unmount()

    expect(unsubscribe).toHaveBeenCalledTimes(1)
  })
})

describe('newlySeen', () => {
  it('returns entries whose id is not already in seen', () => {
    const other: PendingPermission = { ...pending, id: 'p2', session_id: 's2' }
    const result = newlySeen(new Set(['p1']), [pending, other])
    expect(result).toEqual([other])
  })

  it('returns a second prompt in an already-seen session', () => {
    const second: PendingPermission = { ...pending, id: 'p2', tool: 'shell' }
    const result = newlySeen(new Set(['p1']), [pending, second])
    expect(result).toEqual([second])
  })

  it('returns nothing when every entry was already seen (a plain refetch)', () => {
    const result = newlySeen(new Set(['p1']), [pending])
    expect(result).toEqual([])
  })

  it('returns everything when seen is empty (first load)', () => {
    const result = newlySeen(new Set(), [pending])
    expect(result).toEqual([pending])
  })
})

describe('toastSessionLabel', () => {
  it('shows the session title when present', () => {
    expect(toastSessionLabel(pending)).toBe('Gmail cleanup')
  })

  it('falls back to "Chat" when the title is still empty', () => {
    expect(toastSessionLabel({ ...pending, session_title: '' })).toBe('Chat')
  })

  it('falls back to "Mission" for an untitled mission prompt', () => {
    expect(toastSessionLabel({ ...pending, session_title: '', mission_id: 'm1', origin_kind: 'mission' })).toBe('Mission')
  })

  it('appends a non-safe danger level', () => {
    expect(toastSessionLabel({ ...pending, danger: 'destructive' })).toBe('Gmail cleanup · destructive')
  })
})

describe('permissionHref', () => {
  it('links a chat prompt to its session', () => {
    expect(permissionHref(pending)).toBe('/chat/s1')
  })

  it('links a mission prompt to its mission', () => {
    expect(permissionHref({ ...pending, mission_id: 'm1', origin_kind: 'mission' })).toBe('/missions/m1')
  })
})
