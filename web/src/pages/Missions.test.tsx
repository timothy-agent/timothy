import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mission, Notification } from '../api/types'
import type { Signal } from '../lib/events'
import { Missions } from './Missions'

vi.mock('../api/client', () => ({
  listMissions: vi.fn(),
  listNotifications: vi.fn(),
  markNotificationRead: vi.fn(),
}))

vi.mock('../lib/events', () => ({ subscribeEvents: vi.fn() }))

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
  phase: 'generate',
  status: 'working',
  spec: { units: [] },
  progress: [],
  iteration: 1,
  max_iterations: 8,
  consecutive_failures: 0,
  stall_count: 0,
  route: 'default',
  review_route: 'default',
  auto_approve_safe: true,
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
  render(<RouterProvider router={router} />)
  return router
}

afterEach(cleanup)
beforeEach(() => {
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
    expect(banner.closest('div')).toHaveClass('border-amber-200')
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
    expect(banner.closest('div')).toHaveClass('border-green-200')
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
    expect(banner.closest('div')).toHaveClass('border-red-200')
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
    expect(banner.closest('div')).toHaveClass('border-red-200')
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
    expect(banner.closest('div')).toHaveClass('border-amber-200')
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
      schedule_id: 's1',
    }

    it('narrows by kind', async () => {
      vi.mocked(listMissions).mockResolvedValue([mission, coding])
      renderPage()
      await screen.findByText('Fix the login bug')
      await screen.findByText('Refactor the payments module')

      fireEvent.click(screen.getByRole('combobox', { name: 'Filter by kind' }))
      fireEvent.click(await screen.findByRole('option', { name: 'Coding' }))

      expect(screen.queryByText('Fix the login bug')).toBeNull()
      expect(screen.getByText('Refactor the payments module')).toBeTruthy()
      expect(screen.getByText('1 of 2')).toBeTruthy()
    })

    it('narrows by harness, showing Native for an unset harness', async () => {
      vi.mocked(listMissions).mockResolvedValue([mission, coding])
      renderPage()
      await screen.findByText('Fix the login bug')

      fireEvent.click(screen.getByRole('combobox', { name: 'Filter by harness' }))
      fireEvent.click(await screen.findByRole('option', { name: 'Native' }))

      expect(screen.getByText('Fix the login bug')).toBeTruthy()
      expect(screen.queryByText('Refactor the payments module')).toBeNull()
    })

    it('narrows by model', async () => {
      vi.mocked(listMissions).mockResolvedValue([mission, coding])
      renderPage()
      await screen.findByText('Fix the login bug')

      fireEvent.click(screen.getByRole('combobox', { name: 'Filter by model' }))
      fireEvent.click(await screen.findByRole('option', { name: 'claude-opus-4' }))

      expect(screen.queryByText('Fix the login bug')).toBeNull()
      expect(screen.getByText('Refactor the payments module')).toBeTruthy()
    })

    it('narrows by source: automated means schedule_id is set', async () => {
      vi.mocked(listMissions).mockResolvedValue([mission, coding])
      renderPage()
      await screen.findByText('Fix the login bug')

      fireEvent.click(screen.getByRole('combobox', { name: 'Filter by source' }))
      fireEvent.click(await screen.findByRole('option', { name: 'Automated' }))

      expect(screen.queryByText('Fix the login bug')).toBeNull()
      expect(screen.getByText('Refactor the payments module')).toBeTruthy()
    })

    it('shows no count when no filter is active', async () => {
      vi.mocked(listMissions).mockResolvedValue([mission, coding])
      renderPage()
      await screen.findByText('Fix the login bug')
      expect(screen.queryByText('1 of 2')).toBeNull()
      expect(screen.queryByText('2 of 2')).toBeNull()
    })
  })
})
