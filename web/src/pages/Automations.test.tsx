import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Automation, Destination } from '../api/types'
import { TooltipProvider } from '../components/ui/tooltip'
import { Automations } from './Automations'

vi.mock('../api/client', () => ({
  listAutomations: vi.fn(),
  patchAutomation: vi.fn(),
  deleteAutomation: vi.fn(),
  listDestinations: vi.fn(),
}))

import { deleteAutomation, listAutomations, listDestinations, patchAutomation } from '../api/client'

const automation: Automation = {
  id: 's1',
  name: 'weekly-digest',
  description: '',
  agent_id: '00000000-0000-0000-0000-00000000a001',
  action: { kind: 'mission', mission: { goal: 'Summarize the week', kind: 'general', auto_approve_tools: true } },
  concurrency: 'skip',
  max_concurrent: 1,
  max_runs_per_hour: 6,
  continuity: true,
  notes_enabled: true,
  consecutive_failures: 0,
  enabled: true,
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  triggers: [
    {
      id: 't1',
      automation_id: 's1',
      kind: 'cron',
      config: { expr: '0 8 * * 1-5' },
      state: {},
      enabled: true,
      created_at: '2026-07-01T00:00:00Z',
      updated_at: '2026-07-01T00:00:00Z',
    },
  ],
  stats: { runs_total: 0, succeeded_7d: 0, failed_7d: 0, next_run_at: '2026-07-27T08:00:00Z' },
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
  const result = render(
    <TooltipProvider>
      <RouterProvider router={router} />
    </TooltipProvider>,
  )
  return { router, ...result }
}

afterEach(cleanup)
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listAutomations).mockResolvedValue([])
  vi.mocked(listDestinations).mockResolvedValue([])
})

describe('Automations page', () => {
  it('shows an empty state with no automations', async () => {
    renderPage()
    expect(await screen.findByText(/No automations yet/)).toBeTruthy()
  })

  it('lists automations with cron description and next run', async () => {
    vi.mocked(listAutomations).mockResolvedValue([automation])
    renderPage()
    expect(await screen.findByText('weekly-digest')).toBeTruthy()
    expect(screen.getByText('Weekdays, 8:00 AM')).toBeTruthy()
  })

  it('toggles enabled and calls PATCH', async () => {
    vi.mocked(listAutomations).mockResolvedValue([automation])
    vi.mocked(patchAutomation).mockResolvedValue({ ...automation, enabled: false })
    renderPage()
    await screen.findByText('weekly-digest')

    fireEvent.click(screen.getByRole('switch', { name: 'weekly-digest enabled' }))
    await waitFor(() => expect(patchAutomation).toHaveBeenCalledWith('s1', { enabled: false }))
  })

  it('deletes an automation after confirming', async () => {
    vi.mocked(listAutomations).mockResolvedValue([automation])
    vi.mocked(deleteAutomation).mockResolvedValue()
    renderPage()
    await screen.findByText('weekly-digest')

    fireEvent.click(screen.getByRole('button', { name: 'Delete weekly-digest' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(deleteAutomation).toHaveBeenCalledWith('s1'))
  })

  it('shows the last run time on the card when set', async () => {
    vi.mocked(listAutomations).mockResolvedValue([
      {
        ...automation,
        stats: { ...automation.stats, runs_total: 1, last_run_at: '2026-07-20T08:00:00Z', last_run_status: 'failed' },
      },
    ])
    renderPage()
    expect(await screen.findByText(/Last run/)).toBeTruthy()
    expect(screen.getByText('last run failed')).toBeTruthy()
  })

  it('hides the cron line and recurring badge without a cron trigger', async () => {
    vi.mocked(listAutomations).mockResolvedValue([
      {
        ...automation,
        triggers: [{ ...automation.triggers[0], id: 't2', kind: 'manual', config: {} }],
        stats: { runs_total: 0, succeeded_7d: 0, failed_7d: 0 },
      },
    ])
    renderPage()
    await screen.findByText('weekly-digest')
    expect(screen.queryByText('Weekdays, 8:00 AM')).toBeNull()
    expect(screen.queryByText('recurring')).toBeNull()
    expect(screen.queryByText(/last run/)).toBeNull()
  })

  it('falls back to the raw destination id on the card when unresolved', async () => {
    vi.mocked(listDestinations).mockResolvedValue([])
    vi.mocked(listAutomations).mockResolvedValue([{ ...automation, action: { ...automation.action, mission: { ...automation.action.mission, destination_ids: ['unknown-id'] } } }])
    renderPage()
    expect(await screen.findByText('unknown-id')).toBeTruthy()
  })

  it('cancels a delete without calling deleteAutomation', async () => {
    vi.mocked(listAutomations).mockResolvedValue([automation])
    renderPage()
    await screen.findByText('weekly-digest')

    fireEvent.click(screen.getByRole('button', { name: 'Delete weekly-digest' }))
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))

    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
    expect(deleteAutomation).not.toHaveBeenCalled()
  })

  it('navigates to the automation detail page on card click', async () => {
    vi.mocked(listAutomations).mockResolvedValue([automation])
    const { router } = renderPage()
    fireEvent.click(await screen.findByText('weekly-digest'))

    await waitFor(() => expect(router.state.location.pathname).toBe('/automations/s1'))
    expect(await screen.findByText('automation detail page')).toBeTruthy()
  })

  it('shows destination badges when the automation has destination_ids', async () => {
    vi.mocked(listDestinations).mockResolvedValue([destination])
    vi.mocked(listAutomations).mockResolvedValue([{ ...automation, action: { ...automation.action, mission: { ...automation.action.mission, destination_ids: ['d1'] } } }])
    renderPage()
    await screen.findByText('weekly-digest')
    expect(await screen.findByText('ops-inbox')).toBeTruthy()
  })

  it('shows the destination kind icon inside the badge alongside the name', async () => {
    vi.mocked(listDestinations).mockResolvedValue([destination])
    vi.mocked(listAutomations).mockResolvedValue([{ ...automation, action: { ...automation.action, mission: { ...automation.action.mission, destination_ids: ['d1'] } } }])
    renderPage()
    const badge = await screen.findByText('ops-inbox')
    expect(badge.closest('span')?.querySelector('svg')).toBeInTheDocument()
  })

  it('shows no destination badges when the automation has none attached', async () => {
    vi.mocked(listDestinations).mockResolvedValue([destination])
    vi.mocked(listAutomations).mockResolvedValue([automation])
    renderPage()
    await screen.findByText('weekly-digest')
    expect(screen.queryByText('ops-inbox')).toBeNull()
  })

  it('navigates to the edit automation page', async () => {
    vi.mocked(listAutomations).mockResolvedValue([automation])
    const { router } = renderPage()
    await screen.findByText('weekly-digest')

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }))

    await waitFor(() => expect(router.state.location.pathname).toBe('/automations/s1/edit'))
    expect(await screen.findByText('edit automation page')).toBeTruthy()
  })
})
