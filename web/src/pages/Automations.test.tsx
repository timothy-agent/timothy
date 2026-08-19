import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Schedule } from '../api/types'
import { Automations } from './Automations'

vi.mock('../api/client', () => ({
  listSchedules: vi.fn(),
  patchSchedule: vi.fn(),
  deleteSchedule: vi.fn(),
}))

import { deleteSchedule, listSchedules, patchSchedule } from '../api/client'

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

  it('navigates to the edit automation page', async () => {
    vi.mocked(listSchedules).mockResolvedValue([schedule])
    const { router } = renderPage()
    await screen.findByText('weekly-digest')

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }))

    await waitFor(() => expect(router.state.location.pathname).toBe('/automations/s1/edit'))
    expect(await screen.findByText('edit automation page')).toBeTruthy()
  })
})
