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
  listSchedules: vi.fn(),
  listAgents: vi.fn(),
}))

vi.mock('../lib/events', () => ({ subscribeEvents: vi.fn() }))

import { listAgents, listMissions, listNotifications, listSchedules } from '../api/client'
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
  kind: 'research',
  phase: 'execute',
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
  vi.clearAllMocks()
  vi.mocked(subscribeEvents).mockReturnValue(vi.fn())
  vi.mocked(listMissions).mockResolvedValue([mission])
  vi.mocked(listNotifications).mockResolvedValue([])
  vi.mocked(listAgents).mockResolvedValue([])
  vi.mocked(listSchedules).mockResolvedValue([])
})

describe('Missions board', () => {
  it('renders the mission card list', async () => {
    renderPage()
    expect(await screen.findByText('Fix the login bug')).toBeTruthy()
    expect(screen.getByText('working')).toBeTruthy()
  })

  it('shows an unread notification strip', async () => {
    const note: Notification = {
      id: 'n1',
      mission_id: 'm1',
      kind: 'paused',
      message: 'mission m1 is now paused',
      read: false,
      created_at: '2026-01-01T00:00:00Z',
    }
    vi.mocked(listNotifications).mockResolvedValue([note])
    renderPage()
    expect(await screen.findByText('mission m1 is now paused')).toBeTruthy()
  })

  it('does not show read notifications', async () => {
    const note: Notification = {
      id: 'n1',
      mission_id: 'm1',
      kind: 'done',
      message: 'mission m1 is now done',
      read: true,
      created_at: '2026-01-01T00:00:00Z',
    }
    vi.mocked(listNotifications).mockResolvedValue([note])
    renderPage()
    await waitFor(() => expect(listNotifications).toHaveBeenCalled())
    expect(screen.queryByText('mission m1 is now done')).toBeNull()
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
})
