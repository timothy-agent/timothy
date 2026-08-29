import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Destination, Schedule } from '../api/types'
import { Automations } from './Automations'

vi.mock('../api/client', () => ({
  listSchedules: vi.fn(),
  patchSchedule: vi.fn(),
  deleteSchedule: vi.fn(),
  listDestinations: vi.fn(),
}))

import { deleteSchedule, listDestinations, listSchedules, patchSchedule } from '../api/client'

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

const destination: Destination = {
  id: 'd1',
  name: 'ops-inbox',
  kind: 'email',
  config: {},
  credential_ref: '',
  enabled: true,
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
}

function renderPage() {
  const router = createMemoryRouter(
    [
      { path: '/automations', element: <Automations /> },
      { path: '/automations/:id', element: <div>automation detail page</div> },
      { path: '/automations/:id/edit', element: <div>edit automation page</div> },
    ],
    { initialEntries: ['/automations'] },
  )
  const result = render(<RouterProvider router={router} />)
  return { router, ...result }
}

afterEach(cleanup)
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listSchedules).mockResolvedValue([])
  vi.mocked(listDestinations).mockResolvedValue([])
})

describe('Automations page', () => {
  it('shows an empty state with no schedules', async () => {
    renderPage()
    expect(await screen.findByText(/No automations yet/)).toBeTruthy()
  })

  it('lists schedules with cron description and next run', async () => {
    vi.mocked(listSchedules).mockResolvedValue([schedule])
    renderPage()
    expect(await screen.findByText('weekly-digest')).toBeTruthy()
    expect(screen.getByText('Weekdays, 8:00 AM')).toBeTruthy()
  })

  it('toggles enabled and calls PATCH', async () => {
    vi.mocked(listSchedules).mockResolvedValue([schedule])
    vi.mocked(patchSchedule).mockResolvedValue({ ...schedule, enabled: false })
    renderPage()
    await screen.findByText('weekly-digest')

    fireEvent.click(screen.getByRole('switch', { name: 'weekly-digest enabled' }))
    await waitFor(() => expect(patchSchedule).toHaveBeenCalledWith('s1', { enabled: false }))
  })

  it('deletes a schedule after confirming', async () => {
    vi.mocked(listSchedules).mockResolvedValue([schedule])
    vi.mocked(deleteSchedule).mockResolvedValue()
    renderPage()
    await screen.findByText('weekly-digest')

    fireEvent.click(screen.getByRole('button', { name: 'Delete weekly-digest' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(deleteSchedule).toHaveBeenCalledWith('s1'))
  })

  it('navigates to the automation detail page on card click', async () => {
    vi.mocked(listSchedules).mockResolvedValue([schedule])
    const { router } = renderPage()
    fireEvent.click(await screen.findByText('weekly-digest'))

    await waitFor(() => expect(router.state.location.pathname).toBe('/automations/s1'))
    expect(await screen.findByText('automation detail page')).toBeTruthy()
  })

  it('shows destination badges when the schedule has destination_ids', async () => {
    vi.mocked(listDestinations).mockResolvedValue([destination])
    vi.mocked(listSchedules).mockResolvedValue([
      { ...schedule, mission_template: { ...schedule.mission_template, destination_ids: ['d1'] } },
    ])
    renderPage()
    await screen.findByText('weekly-digest')
    expect(await screen.findByText('ops-inbox')).toBeTruthy()
  })

  it('shows the destination kind icon inside the badge alongside the name', async () => {
    vi.mocked(listDestinations).mockResolvedValue([destination])
    vi.mocked(listSchedules).mockResolvedValue([
      { ...schedule, mission_template: { ...schedule.mission_template, destination_ids: ['d1'] } },
    ])
    renderPage()
    const badge = await screen.findByText('ops-inbox')
    expect(badge.closest('span')?.querySelector('svg')).toBeInTheDocument()
  })

  it('shows no destination badges when the schedule has none attached', async () => {
    vi.mocked(listDestinations).mockResolvedValue([destination])
    vi.mocked(listSchedules).mockResolvedValue([schedule])
    renderPage()
    await screen.findByText('weekly-digest')
    expect(screen.queryByText('ops-inbox')).toBeNull()
  })

  it('navigates to the edit automation page', async () => {
    vi.mocked(listSchedules).mockResolvedValue([schedule])
    const { router } = renderPage()
    await screen.findByText('weekly-digest')

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }))

    await waitFor(() => expect(router.state.location.pathname).toBe('/automations/s1/edit'))
    expect(await screen.findByText('edit automation page')).toBeTruthy()
  })
})
