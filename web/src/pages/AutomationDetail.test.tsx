import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminAgent, Destination } from '../api/types'
import { agentID, makeAutomation, makeNote, makeRun, makeTrigger } from '../components/automations/testFixtures'
import { TooltipProvider } from '../components/ui/tooltip'
import { AutomationDetail, runPollMs } from './AutomationDetail'

vi.mock('../api/client', () => ({
  getAutomation: vi.fn(),
  listAutomationRuns: vi.fn(),
  listAutomationNotes: vi.fn(),
  listAgents: vi.fn(),
  listChannels: vi.fn(() => Promise.resolve([])),
  listDestinations: vi.fn(),
  patchAutomation: vi.fn(),
  runAutomationNow: vi.fn(),
  putAutomationNote: vi.fn(),
  deleteAutomationNote: vi.fn(),
}))

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import { toast } from 'sonner'
import {
  deleteAutomationNote,
  getAutomation,
  listAgents,
  listAutomationNotes,
  listAutomationRuns,
  listDestinations,
  patchAutomation,
  putAutomationNote,
  runAutomationNow,
} from '../api/client'

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
      { path: '/automations', element: <div>automations page</div> },
      { path: '/automations/:id', element: <AutomationDetail /> },
      { path: '/automations/:id/edit', element: <div>edit automation page</div> },
      { path: '/missions/:id', element: <div>mission page</div> },
    ],
    { initialEntries: ['/automations/s1'] },
  )
  const result = render(
    <TooltipProvider>
      <RouterProvider router={router} />
    </TooltipProvider>,
  )
  return { router, ...result }
}

// Radix tabs switch on mousedown, not click.
function openTab(name: string) {
  fireEvent.mouseDown(screen.getByRole('tab', { name }), { button: 0 })
}

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getAutomation).mockResolvedValue(makeAutomation())
  vi.mocked(listAutomationRuns).mockResolvedValue([])
  vi.mocked(listAutomationNotes).mockResolvedValue([])
  vi.mocked(listAgents).mockResolvedValue([{ id: agentID, name: 'briefing', enabled: true } as AdminAgent])
  vi.mocked(listDestinations).mockResolvedValue([destination])
})

describe('AutomationDetail header', () => {
  it('shows the name, breadcrumb, agent badge and actions', async () => {
    renderPage()
    expect(await screen.findByRole('heading', { level: 1, name: 'weekly-digest' })).toBeInTheDocument()
    const crumbs = screen.getByRole('navigation', { name: 'Breadcrumb' })
    expect(within(crumbs).getByRole('link', { name: 'Automations' })).toBeInTheDocument()
    expect(within(crumbs).getByText('weekly-digest')).toHaveAttribute('aria-current', 'page')
    expect(await screen.findByText('briefing')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Run now' })).toBeInTheDocument()
    expect(screen.getByRole('switch', { name: 'Enabled' })).toBeChecked()
  })

  it('renders not found for an unknown automation', async () => {
    vi.mocked(getAutomation).mockRejectedValue(new Error('not found'))
    renderPage()
    expect(await screen.findByText('Automation not found.')).toBeInTheDocument()
  })

  it('shows the disabled reason as a tooltip', async () => {
    vi.mocked(getAutomation).mockResolvedValue(
      makeAutomation({ enabled: false, disabled_reason: 'disabled after 3 failed runs in a row' }),
    )
    renderPage()
    const badge = await screen.findByText('disabled')
    fireEvent.focus(badge)
    expect((await screen.findAllByText('disabled after 3 failed runs in a row')).length).toBeGreaterThan(0)
  })

  it('toggles enabled from the header switch', async () => {
    vi.mocked(patchAutomation).mockResolvedValue(makeAutomation({ enabled: false }))
    renderPage()
    fireEvent.click(await screen.findByRole('switch', { name: 'Enabled' }))
    await waitFor(() => expect(patchAutomation).toHaveBeenCalledWith('s1', { enabled: false }))
    await waitFor(() => expect(screen.getByRole('switch', { name: 'Enabled' })).not.toBeChecked())
  })

  it('requests a run and toasts', async () => {
    vi.mocked(runAutomationNow).mockResolvedValue({ event_id: 9 })
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Run now' }))
    await waitFor(() => expect(runAutomationNow).toHaveBeenCalledWith('s1'))
    expect(toast.success).toHaveBeenCalledWith('Run requested')
    await waitFor(() => expect(listAutomationRuns).toHaveBeenCalledTimes(2))
  })

  it('toasts when run now is refused', async () => {
    vi.mocked(runAutomationNow).mockRejectedValue(new Error('automation is disabled or expired'))
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Run now' }))
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not run automation', { description: 'automation is disabled or expired' }),
    )
  })

  it('renames inline', async () => {
    vi.mocked(patchAutomation).mockResolvedValue(makeAutomation({ name: 'daily-digest' }))
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Rename automation' }))
    const input = screen.getByLabelText('Automation name')
    fireEvent.change(input, { target: { value: 'daily-digest' } })
    fireEvent.blur(input)
    await waitFor(() => expect(patchAutomation).toHaveBeenCalledWith('s1', { name: 'daily-digest' }))
  })

  it('opens the editor', async () => {
    const { router } = renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Edit' }))
    expect(router.state.location.pathname).toBe('/automations/s1/edit')
  })
})

describe('AutomationDetail settings tab', () => {
  it('summarizes triggers, action and limits', async () => {
    vi.mocked(getAutomation).mockResolvedValue(
      makeAutomation({
        action: {
          kind: 'mission',
          mission: { goal: 'Summarize the week', kind: 'general', destination_ids: ['d1'], budget_amount: 2, budget_currency: 'USD' },
        },
        concurrency: 'queue',
        max_runs_per_hour: 4,
      }),
    )
    renderPage()
    expect(await screen.findByText('Summarize the week')).toBeInTheDocument()
    expect(screen.getByText('Weekdays, 8:00 AM')).toBeInTheDocument()
    expect(screen.getByText('0 8 * * 1-5')).toBeInTheDocument()
    expect(screen.getByText(/^Next run/)).toBeInTheDocument()
    expect(await screen.findByText('ops-inbox')).toBeInTheDocument()
    expect(screen.getByText('Queue behind the active run')).toBeInTheDocument()
    expect(screen.getByText('Max runs per hour').nextElementSibling).toHaveTextContent('4')
    expect(screen.getByText('Expires').nextElementSibling).toHaveTextContent('Never')
  })
})

describe('AutomationDetail trigger health', () => {
  const hook = makeTrigger({
    id: 't6',
    kind: 'webhook',
    config: { scheme: 'github', filters: [] },
    credential_ref: 'HOOK_KEY',
    state: { last_delivery_at: '2026-07-20T08:00:00Z', last_fired_at: '2026-07-20T08:00:00Z' },
  })
  const connectorID = '0000000c-0000-0000-0000-000000000001'
  const gh = makeTrigger({
    id: 't5',
    kind: 'connector_event',
    config: { connector_id: connectorID, repo: 'octo/timothy', events: ['pr.opened', 'pr.labeled'], labels: [] },
  })

  it('shows the hook path with a copy button and the event descriptions', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    vi.mocked(getAutomation).mockResolvedValue(makeAutomation({ triggers: [gh, hook] }))
    renderPage()
    expect(await screen.findByText('GitHub octo/timothy: pr.opened, pr.labeled')).toBeInTheDocument()
    expect(screen.getByText('Webhook (github)')).toBeInTheDocument()
    expect(screen.getByText('POST /hooks/t6')).toBeInTheDocument()
    expect(screen.getByText(/^Last fired .*Last delivery/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Copy hook path' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('/hooks/t6'))
  })

  it('shows the disabled reason and re-enables with the full trigger set', async () => {
    const tripped = { ...hook, enabled: false, state: { disabled_reason: 'auth_failures' } }
    const automation = makeAutomation({ triggers: [gh, tripped] })
    vi.mocked(getAutomation).mockResolvedValue(automation)
    vi.mocked(patchAutomation).mockResolvedValue(makeAutomation({ triggers: [gh, { ...hook, state: {} }] }))
    renderPage()
    expect(await screen.findByText('Disabled after 20 failed signatures')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Re-enable' }))
    await waitFor(() => expect(patchAutomation).toHaveBeenCalled())
    expect(vi.mocked(patchAutomation).mock.calls[0]).toEqual([
      's1',
      {
        triggers: [
          {
            id: 't5',
            kind: 'connector_event',
            config: { connector_id: connectorID, repo: 'octo/timothy', events: ['pr.opened', 'pr.labeled'] },
            enabled: true,
          },
          { id: 't6', kind: 'webhook', config: { scheme: 'github' }, credential_ref: 'HOOK_KEY', enabled: true },
        ],
      },
    ])
    expect(toast.success).toHaveBeenCalledWith('Trigger re-enabled')
    await waitFor(() => expect(screen.queryByText('Disabled after 20 failed signatures')).toBeNull())
  })

  it('names a rate limited trigger', async () => {
    vi.mocked(getAutomation).mockResolvedValue(
      makeAutomation({ triggers: [{ ...hook, enabled: false, state: { disabled_reason: 'rate_limited' } }] }),
    )
    renderPage()
    expect(await screen.findByText('Disabled: rate limited')).toBeInTheDocument()
  })

  it('toasts a failed re-enable', async () => {
    vi.mocked(getAutomation).mockResolvedValue(
      makeAutomation({ triggers: [{ ...hook, enabled: false, state: { disabled_reason: 'auth_failures' } }] }),
    )
    vi.mocked(patchAutomation).mockRejectedValue(new Error('boom'))
    renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'Re-enable' }))
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Could not re-enable trigger', { description: 'boom' }))
  })
})

describe('AutomationDetail run history tab', () => {
  it('lists runs with pending states and skip reasons', async () => {
    vi.mocked(listAutomationRuns).mockResolvedValue([
      makeRun({ id: 'r3', status: 'queued', started_at: undefined, finished_at: undefined, mission_id: undefined }),
      makeRun({ id: 'r2', status: 'skipped', skip_reason: 'previous run still active', mission_id: undefined }),
      makeRun({ id: 'r1', status: 'done', mission_id: 'm1' }),
    ])
    const { router } = renderPage()
    await screen.findByRole('tab', { name: 'Run history' })
    openTab('Run history')
    expect(await screen.findByText('Queued')).toBeInTheDocument()
    expect(screen.getByText('previous run still active')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('link', { name: /Open the mission/ }))
    expect(router.state.location.pathname).toBe('/missions/m1')
  })

  it('shows an empty state without runs', async () => {
    renderPage()
    await screen.findByRole('tab', { name: 'Run history' })
    openTab('Run history')
    expect(await screen.findByText('No runs yet')).toBeInTheDocument()
  })

  it('refreshes on demand', async () => {
    renderPage()
    await screen.findByRole('tab', { name: 'Run history' })
    openTab('Run history')
    fireEvent.click(await screen.findByRole('button', { name: 'Refresh runs' }))
    await waitFor(() => expect(listAutomationRuns).toHaveBeenCalledTimes(2))
  })

  it('polls while a run is pending and stops once all are terminal', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    vi.mocked(listAutomationRuns)
      .mockResolvedValueOnce([makeRun({ status: 'running', finished_at: undefined })])
      .mockResolvedValue([makeRun({ status: 'done' })])
    renderPage()
    await screen.findByRole('heading', { level: 1, name: 'weekly-digest' })
    expect(listAutomationRuns).toHaveBeenCalledTimes(1)

    await act(async () => {
      vi.advanceTimersByTime(runPollMs)
    })
    await waitFor(() => expect(listAutomationRuns).toHaveBeenCalledTimes(2))

    await act(async () => {
      vi.advanceTimersByTime(runPollMs * 3)
    })
    expect(listAutomationRuns).toHaveBeenCalledTimes(2)
  })

  it('does not poll when every run is terminal', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    vi.mocked(listAutomationRuns).mockResolvedValue([makeRun({ status: 'failed' })])
    renderPage()
    await screen.findByRole('heading', { level: 1, name: 'weekly-digest' })
    await act(async () => {
      vi.advanceTimersByTime(runPollMs * 2)
    })
    expect(listAutomationRuns).toHaveBeenCalledTimes(1)
  })
})

describe('AutomationDetail notes tab', () => {
  it('lists notes with size, count and who updated them', async () => {
    vi.mocked(listAutomationNotes).mockResolvedValue([
      makeNote(),
      makeNote({ name: 'seen', content: 'abc', updated_by_run_id: 'r1' }),
    ])
    vi.mocked(listAutomationRuns).mockResolvedValue([makeRun({ id: 'r1', mission_id: 'm1' })])
    renderPage()
    await screen.findByRole('tab', { name: 'Notes' })
    openTab('Notes')
    expect(await screen.findByText('progress')).toBeInTheDocument()
    expect(screen.getByText('2 of 10')).toBeInTheDocument()
    const seen = document.querySelector('[data-note="seen"]') as HTMLElement
    expect(within(seen).getByText('3 B')).toBeInTheDocument()
    expect(within(seen).getByRole('link', { name: 'Run mission' })).toHaveAttribute('href', '/missions/m1')
    const progress = document.querySelector('[data-note="progress"]') as HTMLElement
    expect(within(progress).getByText('You')).toBeInTheDocument()
  })

  it('adds a note through the dialog', async () => {
    vi.mocked(putAutomationNote).mockResolvedValue(makeNote({ name: 'todo', content: 'x' }))
    renderPage()
    await screen.findByRole('tab', { name: 'Notes' })
    openTab('Notes')
    fireEvent.click(await screen.findByRole('button', { name: 'Add note' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'todo' } })
    fireEvent.change(within(dialog).getByLabelText('Content'), { target: { value: 'x' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(putAutomationNote).toHaveBeenCalledWith('s1', 'todo', 'x'))
    expect(toast.success).toHaveBeenCalledWith('Note saved')
    await waitFor(() => expect(listAutomationNotes).toHaveBeenCalledTimes(2))
  })

  it('edits a note with its name locked', async () => {
    vi.mocked(listAutomationNotes).mockResolvedValue([makeNote()])
    vi.mocked(putAutomationNote).mockResolvedValue(makeNote({ content: 'new' }))
    renderPage()
    await screen.findByRole('tab', { name: 'Notes' })
    openTab('Notes')
    fireEvent.click(await screen.findByRole('button', { name: 'Edit progress' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText('Name')).toBeDisabled()
    fireEvent.change(within(dialog).getByLabelText('Content'), { target: { value: 'new' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(putAutomationNote).toHaveBeenCalledWith('s1', 'progress', 'new'))
  })

  it('keeps the dialog open and toasts when saving fails', async () => {
    vi.mocked(putAutomationNote).mockRejectedValue(new Error('an automation holds at most 10 notes'))
    renderPage()
    await screen.findByRole('tab', { name: 'Notes' })
    openTab('Notes')
    fireEvent.click(await screen.findByRole('button', { name: 'Add note' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'todo' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not save note', { description: 'an automation holds at most 10 notes' }),
    )
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('deletes a note after confirming', async () => {
    vi.mocked(listAutomationNotes).mockResolvedValue([makeNote()])
    vi.mocked(deleteAutomationNote).mockResolvedValue()
    renderPage()
    await screen.findByRole('tab', { name: 'Notes' })
    openTab('Notes')
    fireEvent.click(await screen.findByRole('button', { name: 'Delete progress' }))
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(deleteAutomationNote).toHaveBeenCalledWith('s1', 'progress'))
  })

  it('disables Add note at the cap', async () => {
    vi.mocked(listAutomationNotes).mockResolvedValue(
      Array.from({ length: 10 }, (_, i) => makeNote({ name: `n${i}` })),
    )
    renderPage()
    await screen.findByRole('tab', { name: 'Notes' })
    openTab('Notes')
    expect(await screen.findByText('10 of 10')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Add note' })).toBeDisabled()
  })

  it('says when notes are off', async () => {
    vi.mocked(getAutomation).mockResolvedValue(makeAutomation({ notes_enabled: false }))
    renderPage()
    await screen.findByRole('tab', { name: 'Notes' })
    openTab('Notes')
    expect(await screen.findByText(/Notes are off/)).toBeInTheDocument()
  })
})
