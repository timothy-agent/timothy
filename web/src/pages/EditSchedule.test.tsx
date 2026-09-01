import { cleanup, render, screen } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Schedule } from '../api/types'
import { EditSchedule } from './EditSchedule'

vi.mock('../api/client', () => ({
  listSchedules: vi.fn(),
}))

// The page's own concerns (heading, back link, not-found/loading
// states) don't need MissionForm's real dependency graph — stubbed the
// same way a page test isolates a heavy child component.
vi.mock('../components/missions/MissionForm', () => ({
  MissionForm: () => <div>mission form</div>,
}))

import { listSchedules } from '../api/client'

const schedule: Schedule = {
  id: 's1',
  name: 'weekly-digest',
  cron: '0 8 * * 1-5',
  mission_template: { goal: 'Summarize the week', kind: 'general', auto_approve_tools: true },
  enabled: true,
  next_run: '2026-07-27T08:00:00Z',
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  pending_fire: false,
}

function renderAt(id: string) {
  const router = createMemoryRouter(
    [{ path: '/automations/:id/edit', element: <EditSchedule /> }],
    { initialEntries: [`/automations/${id}/edit`] },
  )
  return render(<RouterProvider router={router} />)
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listSchedules).mockResolvedValue([])
})

describe('EditSchedule', () => {
  it('shows the "Edit automation" heading and a back link to the automation detail page', async () => {
    vi.mocked(listSchedules).mockResolvedValue([schedule])
    renderAt('s1')

    expect(await screen.findByRole('heading', { name: 'Edit automation' })).toBeTruthy()
    const back = screen.getByRole('link', { name: /Automation/ })
    expect(back.getAttribute('href')).toBe('/automations/s1')
  })

  it('shows a not-found message for an unknown schedule', async () => {
    renderAt('missing')
    expect(await screen.findByText('Schedule not found.')).toBeTruthy()
  })
})
