import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Schedule } from '../../api/types'
import { RecurringSchedules } from './RecurringSchedules'

vi.mock('../../api/client', () => ({
  listSchedules: vi.fn(),
  createSchedule: vi.fn(),
  patchSchedule: vi.fn(),
  deleteSchedule: vi.fn(),
  listAgents: vi.fn(),
}))

import { deleteSchedule, listAgents, listSchedules, patchSchedule } from '../../api/client'

const schedule: Schedule = {
  id: 's1',
  name: 'weekly-digest',
  cron: '0 8 * * 1-5',
  mission_template: { goal: 'Summarize the week', kind: 'research', auto_approve_safe: true },
  enabled: true,
  next_run: '2026-07-27T08:00:00Z',
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
}

function renderList() {
  return render(
    <MemoryRouter>
      <RecurringSchedules />
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listSchedules).mockResolvedValue([])
  vi.mocked(listAgents).mockResolvedValue([])
})

describe('RecurringSchedules', () => {
  it('renders nothing when there are no schedules', async () => {
    const { container } = renderList()
    await waitFor(() => expect(listSchedules).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  it('lists schedules with cron description and next run', async () => {
    vi.mocked(listSchedules).mockResolvedValue([schedule])
    renderList()
    expect(await screen.findByText('weekly-digest')).toBeTruthy()
    expect(screen.getByText('Weekdays, 8:00 AM')).toBeTruthy()
  })

  it('toggles enabled optimistically', async () => {
    vi.mocked(listSchedules).mockResolvedValue([schedule])
    vi.mocked(patchSchedule).mockResolvedValue({ ...schedule, enabled: false })
    renderList()
    await screen.findByText('weekly-digest')

    fireEvent.click(screen.getByRole('switch', { name: 'weekly-digest enabled' }))
    await waitFor(() => expect(patchSchedule).toHaveBeenCalledWith('s1', { enabled: false }))
  })

  it('deletes a schedule after confirming', async () => {
    vi.mocked(listSchedules).mockResolvedValue([schedule])
    vi.mocked(deleteSchedule).mockResolvedValue()
    renderList()
    await screen.findByText('weekly-digest')

    fireEvent.click(screen.getByRole('button', { name: 'Delete weekly-digest' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(deleteSchedule).toHaveBeenCalledWith('s1'))
  })
})
