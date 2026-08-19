import { cleanup, render, screen } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mission, Schedule } from '../api/types'
import { AutomationDetail } from './AutomationDetail'

vi.mock('../api/client', () => ({
  listSchedules: vi.fn(),
  listMissions: vi.fn(),
}))

import { listMissions, listSchedules } from '../api/client'

const schedule: Schedule = {
  id: 's1',
  name: 'weekly-digest',
  cron: '0 8 * * 1-5',
  mission_template: { goal: 'Summarize the week', kind: 'general', auto_approve_safe: true },
  enabled: true,
  next_run: '2026-07-27T08:00:00Z',
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  pending_fire: false,
}

const firedMission: Mission = {
  id: 'm1',
  goal: 'Summarize the week',
  kind: 'general',
  phase: 'done',
  status: 'done',
  spec: { units: [] },
  progress: [],
  iteration: 1,
  max_iterations: 8,
  consecutive_failures: 0,
  stall_count: 0,
  route: 'default',
  review_route: 'default',
  auto_approve_safe: true,
  schedule_id: 's1',
  created_at: '2026-07-27T08:00:00Z',
  updated_at: '2026-07-27T08:01:00Z',
}

function renderAt(id: string) {
  const router = createMemoryRouter(
    [{ path: '/automations/:id', element: <AutomationDetail /> }],
    { initialEntries: [`/automations/${id}`] },
  )
  return render(<RouterProvider router={router} />)
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listSchedules).mockResolvedValue([])
  vi.mocked(listMissions).mockResolvedValue([])
})

describe('AutomationDetail', () => {
  it('shows a not-found message for an unknown schedule', async () => {
    renderAt('missing')
    expect(await screen.findByText('Automation not found.')).toBeTruthy()
  })

  it('renders the schedule summary and fetches its mission history', async () => {
    vi.mocked(listSchedules).mockResolvedValue([schedule])
    vi.mocked(listMissions).mockResolvedValue([firedMission])
    renderAt('s1')

    expect(await screen.findByText('weekly-digest')).toBeTruthy()
    expect(screen.getByText('Weekdays, 8:00 AM')).toBeTruthy()
    expect((await screen.findAllByText('Summarize the week')).length).toBe(2)
    expect(listMissions).toHaveBeenCalledWith({ scheduleId: 's1' })
  })

  it('shows an empty state when the schedule has never fired', async () => {
    vi.mocked(listSchedules).mockResolvedValue([schedule])
    renderAt('s1')
    expect(await screen.findByText('No missions fired yet.')).toBeTruthy()
  })
})
