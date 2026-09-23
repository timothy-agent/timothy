import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Automation, AutomationRun, Destination } from '../api/types'
import { TooltipProvider } from '../components/ui/tooltip'
import { AutomationDetail } from './AutomationDetail'

vi.mock('../api/client', () => ({
  getAutomation: vi.fn(),
  listAutomationRuns: vi.fn(),
  listDestinations: vi.fn(),
  patchAutomation: vi.fn(),
  runAutomationNow: vi.fn(),
}))

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import { toast } from 'sonner'
import { getAutomation, listAutomationRuns, listDestinations, patchAutomation, runAutomationNow } from '../api/client'

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

const runs: AutomationRun[] = [
  {
    id: 'r2',
    automation_id: 's1',
    trigger_id: 't1',
    dedup_key: 'cron:2026-07-27T08:00:00Z',
    status: 'done',
    event: {},
    mission_id: 'm1',
    created_at: '2026-07-27T08:00:00Z',
    started_at: '2026-07-27T08:00:01Z',
    finished_at: '2026-07-27T08:05:00Z',
  },
  {
    id: 'r1',
    automation_id: 's1',
    trigger_id: 't1',
    dedup_key: 'cron:2026-07-26T08:00:00Z',
    status: 'skipped',
    skip_reason: 'previous run still active',
    event: {},
    created_at: '2026-07-26T08:00:00Z',
  },
]

function renderAt(id: string) {
  const router = createMemoryRouter(
    [{ path: '/automations/:id', element: <AutomationDetail /> }],
    { initialEntries: [`/automations/${id}`] },
  )
  return render(
    <TooltipProvider>
      <RouterProvider router={router} />
    </TooltipProvider>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getAutomation).mockRejectedValue(new Error('automation not found'))
  vi.mocked(listAutomationRuns).mockResolvedValue([])
  vi.mocked(listDestinations).mockResolvedValue([])
})

describe('AutomationDetail', () => {
  it('shows a not-found message for an unknown automation', async () => {
    renderAt('missing')
    expect(await screen.findByText('Automation not found.')).toBeTruthy()
  })

  it('renders the automation summary and its runs with mission links', async () => {
    vi.mocked(getAutomation).mockResolvedValue(automation)
    vi.mocked(listAutomationRuns).mockResolvedValue(runs)
    renderAt('s1')

    expect(await screen.findByRole('heading', { name: 'weekly-digest' })).toBeTruthy()
    expect(screen.getByText('Weekdays, 8:00 AM')).toBeTruthy()
    expect(screen.getByText('Summarize the week')).toBeTruthy()
    expect(getAutomation).toHaveBeenCalledWith('s1')
    expect(listAutomationRuns).toHaveBeenCalledWith('s1')

    const list = await screen.findByRole('list', { name: 'Runs' })
    const rows = within(list).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(within(rows[0]).getByText('done')).toBeTruthy()
    expect(within(rows[0]).getByRole('link', { name: 'View mission' }).getAttribute('href')).toBe('/missions/m1')
    expect(within(rows[1]).getByText('skipped')).toBeTruthy()
    expect(within(rows[1]).getByText('previous run still active')).toBeTruthy()
    expect(within(rows[1]).queryByRole('link')).toBeNull()
  })

  it('shows an empty state when the automation has never run', async () => {
    vi.mocked(getAutomation).mockResolvedValue(automation)
    renderAt('s1')
    expect(await screen.findByText('No runs yet.')).toBeTruthy()
  })

  it('hides the cron line when the automation has no cron trigger', async () => {
    vi.mocked(getAutomation).mockResolvedValue({ ...automation, triggers: [], stats: { ...automation.stats, next_run_at: undefined } })
    renderAt('s1')
    await screen.findByRole('heading', { name: 'weekly-digest' })
    expect(screen.queryByText('Weekdays, 8:00 AM')).toBeNull()
  })

  it('requests a run on Run now and toasts', async () => {
    vi.mocked(getAutomation).mockResolvedValue(automation)
    vi.mocked(runAutomationNow).mockResolvedValue({ event_id: 7 })
    renderAt('s1')
    fireEvent.click(await screen.findByRole('button', { name: 'Run now' }))

    await waitFor(() => expect(runAutomationNow).toHaveBeenCalledWith('s1'))
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Run requested'))
  })

  it('toasts an error when Run now is refused', async () => {
    vi.mocked(getAutomation).mockResolvedValue(automation)
    vi.mocked(runAutomationNow).mockRejectedValue(new Error('automation is disabled or expired'))
    renderAt('s1')
    fireEvent.click(await screen.findByRole('button', { name: 'Run now' }))

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Could not run automation', expect.anything()))
  })

  it('shows destination badges when the automation has destination_ids', async () => {
    vi.mocked(listDestinations).mockResolvedValue([destination])
    vi.mocked(getAutomation).mockResolvedValue({ ...automation, action: { ...automation.action, mission: { ...automation.action.mission, destination_ids: ['d1'] } } })
    renderAt('s1')
    expect(await screen.findByText('ops-inbox')).toBeTruthy()
  })

  it('shows no destination badges when the automation has none attached', async () => {
    vi.mocked(listDestinations).mockResolvedValue([destination])
    vi.mocked(getAutomation).mockResolvedValue(automation)
    renderAt('s1')
    await screen.findByRole('heading', { name: 'weekly-digest' })
    expect(screen.queryByText('ops-inbox')).toBeNull()
  })

  it('falls back to the raw destination id when it cannot be resolved', async () => {
    vi.mocked(listDestinations).mockResolvedValue([])
    vi.mocked(getAutomation).mockResolvedValue({ ...automation, action: { ...automation.action, mission: { ...automation.action.mission, destination_ids: ['unknown-id'] } } })
    renderAt('s1')
    expect(await screen.findByText('unknown-id')).toBeTruthy()
  })

  it('shows a disabled badge when the automation is disabled', async () => {
    vi.mocked(getAutomation).mockResolvedValue({ ...automation, enabled: false })
    renderAt('s1')
    expect(await screen.findByText('disabled')).toBeTruthy()
  })

  it('shows the last run time when set', async () => {
    vi.mocked(getAutomation).mockResolvedValue({
      ...automation,
      stats: { ...automation.stats, runs_total: 1, last_run_at: '2026-07-20T08:00:00Z', last_run_status: 'done' },
    })
    renderAt('s1')
    expect(await screen.findByText(/Last run/)).toBeTruthy()
  })

  it('navigates to the edit page when Edit is clicked', async () => {
    vi.mocked(getAutomation).mockResolvedValue(automation)
    const router = createMemoryRouter(
      [
        { path: '/automations/:id', element: <AutomationDetail /> },
        { path: '/automations/:id/edit', element: <div>edit page</div> },
      ],
      { initialEntries: ['/automations/s1'] },
    )
    render(
      <TooltipProvider>
        <RouterProvider router={router} />
      </TooltipProvider>,
    )
    fireEvent.click(await screen.findByRole('button', { name: 'Edit' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/automations/s1/edit'))
  })

  it('renames the automation: pencil click, edit, Enter saves the trimmed name', async () => {
    vi.mocked(getAutomation).mockResolvedValue(automation)
    vi.mocked(patchAutomation).mockResolvedValue({ ...automation, name: 'New Name' })
    renderAt('s1')
    await screen.findByRole('heading', { name: 'weekly-digest' })

    fireEvent.click(screen.getByRole('button', { name: 'Rename automation' }))
    const input = screen.getByRole('textbox', { name: 'Automation name' })
    fireEvent.change(input, { target: { value: 'New Name' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await waitFor(() => expect(patchAutomation).toHaveBeenCalledWith('s1', { name: 'New Name' }))
  })

  it('cancels the rename on Escape without calling patchAutomation', async () => {
    vi.mocked(getAutomation).mockResolvedValue(automation)
    renderAt('s1')
    await screen.findByRole('heading', { name: 'weekly-digest' })

    fireEvent.click(screen.getByRole('button', { name: 'Rename automation' }))
    const input = screen.getByRole('textbox', { name: 'Automation name' })
    fireEvent.change(input, { target: { value: 'New Name' } })
    fireEvent.keyDown(input, { key: 'Escape' })

    expect(screen.queryByRole('textbox', { name: 'Automation name' })).toBeNull()
    expect(screen.getByRole('heading', { name: 'weekly-digest' })).toBeTruthy()
    expect(patchAutomation).not.toHaveBeenCalled()
  })
})
