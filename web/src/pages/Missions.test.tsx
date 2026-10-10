import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mission, Notification } from '../api/types'
import { TooltipProvider } from '../components/ui/tooltip'
import type { Signal } from '../lib/events'
import { Missions } from './Missions'

vi.mock('../api/client', () => ({
  listMissions: vi.fn(),
  listNotifications: vi.fn(),
  markNotificationRead: vi.fn(),
}))

vi.mock('../lib/events', () => ({ subscribeEvents: vi.fn() }))

vi.mock('../onboarding/context', async () => {
  const { onboardingState } = await import('../onboarding/testing')
  return { useOnboarding: () => onboardingState() }
})

import { listMissions, listNotifications } from '../api/client'
import { subscribeEvents } from '../lib/events'

// captureSubscribe grabs the onSignal/onReady callbacks subscribeEvents
// was last called with, so a test can fire them directly instead of
// waiting on a real SSE stream.
function captureSubscribe() {
  const unsubscribe = vi.fn()
  vi.mocked(subscribeEvents).mockReturnValue(unsubscribe)
  return {
    fireSignal: (sig: Signal) => vi.mocked(subscribeEvents).mock.calls.at(-1)?.[0](sig),
    fireReady: () => vi.mocked(subscribeEvents).mock.calls.at(-1)?.[1]?.(),
    unsubscribe,
  }
}

const mission: Mission = {
  id: 'm1',
  goal: 'Fix the login bug',
  kind: 'general',
  phase: 'build',
  status: 'working',
  plan: { units: [] },
  progress: [],
  iteration: 1,
  max_iterations: 8,
  consecutive_failures: 0,
  stall_count: 0,
  route: 'default',
  review_route: 'default',
  auto_approve_tools: true,
  auto_approve_plan: true,
  asks_used: 0,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

function renderPage() {
  const router = createMemoryRouter(
    [
      { path: '/missions', element: <Missions /> },
      { path: '/missions/new', element: <div>new mission page</div> },
    ],
    { initialEntries: ['/missions'] },
  )
  render(
    <TooltipProvider>
      <RouterProvider router={router} />
    </TooltipProvider>,
  )
  return router
}

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

afterEach(cleanup)
beforeEach(() => {
  observerCallbacks = []
  vi.stubGlobal('IntersectionObserver', FakeIntersectionObserver)
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(subscribeEvents).mockReturnValue(vi.fn())
  vi.mocked(listMissions).mockResolvedValue([mission])
  vi.mocked(listNotifications).mockResolvedValue([])
})

describe('Missions board', () => {
  it('renders the mission card list', async () => {
    renderPage()
    expect(await screen.findByText('Fix the login bug')).toBeTruthy()
    expect(screen.getByText('working')).toBeTruthy()
  })

  it('requests unread notifications only', async () => {
    renderPage()
    await waitFor(() => expect(listNotifications).toHaveBeenCalledWith({ unread: true }))
  })

  it('shows an unread notification strip, colored amber for paused', async () => {
    const note: Notification = {
      id: 'n1',
      mission_id: 'm1',
      kind: 'paused',
      message: 'Mission - Fix the login bug is paused, needs your intervention.',
      read: false,
      created_at: '2026-01-01T00:00:00Z',
    }
    vi.mocked(listNotifications).mockResolvedValue([note])
    renderPage()
    const banner = await screen.findByText(
      'Mission - Fix the login bug is paused, needs your intervention.',
    )
    expect(banner.closest('[data-tone]')).toHaveAttribute('data-tone', 'warning')
  })

  it('renders an operator notification without a mission link', async () => {
    const note: Notification = {
      id: 'n2',
      kind: 'automation_trigger_disabled',
      message: 'Deploy hook: webhook trigger disabled after 20 failed signatures in 10m0s',
      read: false,
      created_at: '2026-01-01T00:00:00Z',
    }
    vi.mocked(listNotifications).mockResolvedValue([note])
    renderPage()
    const text = await screen.findByText(
      'Deploy hook: webhook trigger disabled after 20 failed signatures in 10m0s',
    )
    expect(text.closest('button')).toBeNull()
    expect(text.closest('[data-tone]')).toHaveAttribute('data-tone', 'warning')
    expect(screen.getByRole('button', { name: 'Dismiss' })).toBeTruthy()
  })

  it('does not show read notifications', async () => {
    const note: Notification = {
      id: 'n1',
      mission_id: 'm1',
      kind: 'done',
      message: 'Mission - Fix the login bug is done',
      read: true,
      created_at: '2026-01-01T00:00:00Z',
    }
    vi.mocked(listNotifications).mockResolvedValue([note])
    renderPage()
    await waitFor(() => expect(listNotifications).toHaveBeenCalled())
    expect(screen.queryByText('Mission - Fix the login bug is done')).toBeNull()
  })

  it('colors a done notification green', async () => {
    const note: Notification = {
      id: 'n1',
      mission_id: 'm1',
      kind: 'done',
      message: 'Mission - Fix the login bug is done',
      read: false,
      created_at: '2026-01-01T00:00:00Z',
    }
    vi.mocked(listNotifications).mockResolvedValue([note])
    renderPage()
    const banner = await screen.findByText('Mission - Fix the login bug is done')
    expect(banner.closest('[data-tone]')).toHaveAttribute('data-tone', 'good')
  })

  it('colors an error notification red', async () => {
    const note: Notification = {
      id: 'n1',
      mission_id: 'm1',
      kind: 'error',
      message: 'Mission - Fix the login bug is failed',
      read: false,
      created_at: '2026-01-01T00:00:00Z',
    }
    vi.mocked(listNotifications).mockResolvedValue([note])
    renderPage()
    const banner = await screen.findByText('Mission - Fix the login bug is failed')
    expect(banner.closest('[data-tone]')).toHaveAttribute('data-tone', 'destructive')
  })

  it('colors a cancelled (error kind) notification red', async () => {
    const note: Notification = {
      id: 'n1',
      mission_id: 'm1',
      kind: 'error',
      message: 'Mission - Fix the login bug is cancelled',
      read: false,
      created_at: '2026-01-01T00:00:00Z',
    }
    vi.mocked(listNotifications).mockResolvedValue([note])
    renderPage()
    const banner = await screen.findByText('Mission - Fix the login bug is cancelled')
    expect(banner.closest('[data-tone]')).toHaveAttribute('data-tone', 'destructive')
  })

  it('colors a waiting_for_input notification amber', async () => {
    const note: Notification = {
      id: 'n1',
      mission_id: 'm1',
      kind: 'waiting_for_input',
      message: 'Mission - Fix the login bug is waiting for your input.',
      read: false,
      created_at: '2026-01-01T00:00:00Z',
    }
    vi.mocked(listNotifications).mockResolvedValue([note])
    renderPage()
    const banner = await screen.findByText(
      'Mission - Fix the login bug is waiting for your input.',
    )
    expect(banner.closest('[data-tone]')).toHaveAttribute('data-tone', 'warning')
  })

  it('navigates to the new mission page', async () => {
    const router = renderPage()
    await screen.findByText('Fix the login bug')

    fireEvent.click(screen.getByRole('button', { name: 'New mission' }))

    await waitFor(() => expect(router.state.location.pathname).toBe('/missions/new'))
    expect(await screen.findByText('new mission page')).toBeTruthy()
  })

  it('refetches missions and notifications on any signal', async () => {
    const sub = captureSubscribe()
    renderPage()
    await screen.findByText('Fix the login bug')
    vi.mocked(listMissions).mockClear()
    vi.mocked(listNotifications).mockClear()

    sub.fireSignal({ kind: 'mission', id: 'm1' })

    await waitFor(() => expect(listMissions).toHaveBeenCalledTimes(1))
    expect(listNotifications).toHaveBeenCalledTimes(1)
  })

  it('refetches on the ready event (initial connect and reconnects)', async () => {
    const sub = captureSubscribe()
    renderPage()
    await screen.findByText('Fix the login bug')
    vi.mocked(listMissions).mockClear()

    sub.fireReady()

    await waitFor(() => expect(listMissions).toHaveBeenCalledTimes(1))
  })

  it('unsubscribes on unmount', async () => {
    const sub = captureSubscribe()
    const { unmount } = render(
      <RouterProvider
        router={createMemoryRouter(
          [{ path: '/missions', element: <Missions /> }],
          { initialEntries: ['/missions'] },
        )}
      />,
    )
    await screen.findByText('Fix the login bug')
    unmount()
    expect(sub.unsubscribe).toHaveBeenCalled()
  })

  describe('filters', () => {
    const coding: Mission = {
      ...mission,
      id: 'm2',
      goal: 'Refactor the payments module',
      kind: 'coding',
      harness: 'claude-cli',
      top_model: 'claude-opus-4',
      automation_run_id: 'r1',
      origin_kind: 'automation',
    }

    // The server applies the filters; the mock answers like it would.
    beforeEach(() => {
      vi.mocked(listMissions).mockImplementation(async (opts) => {
        if (opts?.kind === 'coding' || opts?.harness === 'claude-cli') return [coding]
        if (opts?.model === 'claude-opus-4' || opts?.source === 'automated') return [coding]
        if (opts?.harness === 'native' || opts?.source === 'manual') return [mission]
        return [mission, coding]
      })
    })

    const pick = async (combobox: string, option: string) => {
      await screen.findByText('Refactor the payments module')
      fireEvent.click(screen.getByRole('combobox', { name: combobox }))
      fireEvent.click(await screen.findByRole('option', { name: option }))
    }

    it('narrows by kind on the server', async () => {
      renderPage()
      await pick('Filter by kind', 'Coding')
      await waitFor(() => expect(screen.queryByText('Fix the login bug')).toBeNull())
      expect(screen.getByText('Refactor the payments module')).toBeTruthy()
      expect(listMissions).toHaveBeenLastCalledWith(
        expect.objectContaining({ kind: 'coding', limit: 50 }),
      )
    })

    it('sends harness=native for the Native option', async () => {
      renderPage()
      await pick('Filter by harness', 'Native')
      await waitFor(() => expect(screen.queryByText('Refactor the payments module')).toBeNull())
      expect(screen.getByText('Fix the login bug')).toBeTruthy()
      expect(listMissions).toHaveBeenLastCalledWith(expect.objectContaining({ harness: 'native' }))
    })

    it('keeps other harness options after narrowing', async () => {
      renderPage()
      await pick('Filter by harness', 'Native')
      await waitFor(() => expect(screen.queryByText('Refactor the payments module')).toBeNull())
      fireEvent.click(screen.getByRole('combobox', { name: 'Filter by harness' }))
      expect(await screen.findByRole('option', { name: 'claude-cli' })).toBeTruthy()
    })

    it('narrows by model on the server', async () => {
      renderPage()
      await pick('Filter by model', 'claude-opus-4')
      await waitFor(() => expect(screen.queryByText('Fix the login bug')).toBeNull())
      expect(listMissions).toHaveBeenLastCalledWith(
        expect.objectContaining({ model: 'claude-opus-4' }),
      )
    })

    it('narrows by source: automated', async () => {
      renderPage()
      await pick('Filter by source', 'Automated')
      await waitFor(() => expect(screen.queryByText('Fix the login bug')).toBeNull())
      expect(listMissions).toHaveBeenLastCalledWith(expect.objectContaining({ source: 'automated' }))
    })

    it('narrows by source: manual', async () => {
      renderPage()
      await pick('Filter by source', 'Manual')
      await waitFor(() => expect(screen.queryByText('Refactor the payments module')).toBeNull())
      expect(screen.getByText('Fix the login bug')).toBeTruthy()
      expect(listMissions).toHaveBeenLastCalledWith(expect.objectContaining({ source: 'manual' }))
    })

    it('sends no filter params when none is active', async () => {
      renderPage()
      await screen.findByText('Refactor the payments module')
      expect(listMissions).toHaveBeenCalledWith({
        kind: undefined,
        harness: undefined,
        model: undefined,
        source: undefined,
        limit: 50,
      })
    })

    it('shows a filters-no-match empty state with no create button in it', async () => {
      renderPage()
      await screen.findByText('Refactor the payments module')
      vi.mocked(listMissions).mockResolvedValue([])
      fireEvent.click(screen.getByRole('combobox', { name: 'Filter by kind' }))
      fireEvent.click(await screen.findByRole('option', { name: 'General' }))

      const empty = (await screen.findByText('No missions match the current filters.')).closest('div')
      expect(empty?.querySelector('button')).toBeNull()
    })
  })

  describe('paging', () => {
    // page builds n missions, newest first, starting `start` minutes
    // before a fixed instant.
    const page = (prefix: string, n: number, start = 0): Mission[] =>
      Array.from({ length: n }, (_, i) => ({
        ...mission,
        id: `${prefix}-${String(i).padStart(3, '0')}`,
        goal: `${prefix} mission ${i}`,
        created_at: new Date(Date.UTC(2026, 9, 1, 12, 0) - (start + i) * 60_000).toISOString(),
      }))

    const first = page('p1', 50)
    const second = page('p2', 10, 50)

    it('loads the next page at the sentinel and appends without duplicates', async () => {
      vi.mocked(listMissions).mockImplementation(async (opts) =>
        // The second page repeats the first page's last row.
        opts?.cursor ? [first[49], ...second] : first,
      )
      renderPage()
      await screen.findByText('p1 mission 0')
      expect(screen.getByTestId('missions-sentinel')).toBeTruthy()

      scrollToSentinel()

      await screen.findByText('p2 mission 9')
      expect(listMissions).toHaveBeenLastCalledWith(
        expect.objectContaining({
          limit: 50,
          cursor: { before: first[49].created_at, beforeId: first[49].id },
        }),
      )
      expect(screen.getAllByText('p1 mission 49')).toHaveLength(1)
      // A short page means the end: the sentinel goes away.
      expect(screen.queryByTestId('missions-sentinel')).toBeNull()
    })

    it('shows no sentinel when the first page is short', async () => {
      renderPage()
      await screen.findByText('Fix the login bug')
      expect(screen.queryByTestId('missions-sentinel')).toBeNull()
    })

    it('restarts from page 1 when a filter changes', async () => {
      vi.mocked(listMissions).mockImplementation(async (opts) => {
        if (opts?.kind === 'coding') return page('coding', 3)
        return opts?.cursor ? second : first
      })
      renderPage()
      await screen.findByText('p1 mission 0')
      scrollToSentinel()
      await screen.findByText('p2 mission 0')

      fireEvent.click(screen.getByRole('combobox', { name: 'Filter by kind' }))
      fireEvent.click(await screen.findByRole('option', { name: 'Coding' }))

      await screen.findByText('coding mission 0')
      expect(vi.mocked(listMissions).mock.lastCall?.[0]?.cursor).toBeUndefined()
      expect(screen.queryByText('p1 mission 0')).toBeNull()
      expect(screen.queryByText('p2 mission 0')).toBeNull()
    })

    it('a signal refetches only page 1 and keeps older loaded pages', async () => {
      const sub = captureSubscribe()
      vi.mocked(listMissions).mockImplementation(async (opts) => (opts?.cursor ? second : first))
      renderPage()
      await screen.findByText('p1 mission 0')
      scrollToSentinel()
      await screen.findByText('p2 mission 9')

      vi.mocked(listMissions).mockClear()
      // A new mission pushes first[49] off page 1.
      vi.mocked(listMissions).mockResolvedValue([...page('new', 1, -1), ...first.slice(0, 49)])

      sub.fireSignal({ kind: 'mission', id: 'new-000' })

      await screen.findByText('new mission 0')
      expect(listMissions).toHaveBeenCalledTimes(1)
      expect(vi.mocked(listMissions).mock.lastCall?.[0]?.cursor).toBeUndefined()
      expect(screen.getByText('p1 mission 49')).toBeTruthy()
      expect(screen.getByText('p2 mission 9')).toBeTruthy()
      expect(screen.getAllByText('p1 mission 0')).toHaveLength(1)
    })
  })

  it('shows a no-missions empty state with a working create button', async () => {
    vi.mocked(listMissions).mockResolvedValue([])
    const router = renderPage()
    const heading = await screen.findByText('No missions yet')
    expect(screen.getByText('A mission is a longer task Timothy works on by itself and reports back.')).toBeTruthy()

    const createButton = heading.closest('div')?.querySelector('button')
    expect(createButton).toBeTruthy()
    fireEvent.click(createButton!)
    await waitFor(() => expect(router.state.location.pathname).toBe('/missions/new'))
  })
})
