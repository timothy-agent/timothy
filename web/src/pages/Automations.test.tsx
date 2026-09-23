import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminAgent, AutomationsStats } from '../api/types'
import { agentID, makeAutomation, makeTrigger } from '../components/automations/testFixtures'
import { TooltipProvider } from '../components/ui/tooltip'
import { Automations } from './Automations'

vi.mock('../api/client', () => ({
  listAutomations: vi.fn(),
  automationsStats: vi.fn(),
  listAutomationTemplates: vi.fn(),
  listAgents: vi.fn(),
  patchAutomation: vi.fn(),
  deleteAutomation: vi.fn(),
  runAutomationNow: vi.fn(),
}))

vi.mock('../components/charts/EChart', () => ({
  EChart: ({ option }: { option: { series: { name: string }[] } }) => (
    <div data-testid="sparkline">{option.series.map((s) => s.name).join(',')}</div>
  ),
}))

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import { toast } from 'sonner'
import {
  automationsStats,
  deleteAutomation,
  listAgents,
  listAutomations,
  listAutomationTemplates,
  patchAutomation,
  runAutomationNow,
} from '../api/client'

const stats: AutomationsStats = {
  total: 4,
  enabled: 3,
  succeeded_7d: 11,
  failed_7d: 2,
  sparkline: [
    { day: '2026-09-22', succeeded: 5, failed: 1 },
    { day: '2026-09-23', succeeded: 6, failed: 1 },
  ],
}

const agent = { id: agentID, name: 'briefing', enabled: true, is_default: true } as AdminAgent

function renderPage() {
  const router = createMemoryRouter(
    [
      { path: '/automations', element: <Automations /> },
      { path: '/automations/new', element: <div>new automation page</div> },
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

const row = (id: string) => document.querySelector(`[data-automation-id="${id}"]`) as HTMLElement

async function openMenu(name: string) {
  fireEvent.pointerDown(await screen.findByRole('button', { name: `Actions for ${name}` }), { button: 0, pointerId: 1 })
}

afterEach(cleanup)
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listAutomations).mockResolvedValue([])
  vi.mocked(automationsStats).mockResolvedValue(stats)
  vi.mocked(listAutomationTemplates).mockRejectedValue(new Error('not found'))
  vi.mocked(listAgents).mockResolvedValue([agent])
})

describe('Automations page', () => {
  it('shows stat tiles from the stats endpoint with a two-series sparkline', async () => {
    renderPage()
    const total = await screen.findByText('Total')
    expect(total.parentElement).toHaveTextContent('4')
    expect(screen.getByText('Enabled').parentElement).toHaveTextContent('3')
    expect(screen.getByText('Succeeded (7d)').parentElement).toHaveTextContent('11')
    expect(screen.getByText('Failed (7d)').parentElement).toHaveTextContent('2')
    expect(screen.getByText('Runs, 14 days').parentElement).toHaveTextContent('13')
    expect(screen.getByTestId('sparkline')).toHaveTextContent('succeeded,failed')
  })

  it('shows N/A tiles when stats fail', async () => {
    vi.mocked(automationsStats).mockRejectedValue(new Error('down'))
    renderPage()
    await waitFor(() => expect(screen.getByText('Total').parentElement).toHaveTextContent('N/A'))
    expect(screen.queryByTestId('sparkline')).toBeNull()
  })

  it('shows an empty state with no automations', async () => {
    renderPage()
    expect(await screen.findByText('No automations yet')).toBeInTheDocument()
  })

  it('renders a table row with agent, trigger badges, last run and created', async () => {
    vi.mocked(listAutomations).mockResolvedValue([
      makeAutomation({
        triggers: [makeTrigger(), makeTrigger({ id: 't2', kind: 'manual', config: {} })],
        stats: { runs_total: 1, succeeded_7d: 0, failed_7d: 1, last_run_at: '2026-07-20T08:00:00Z', last_run_status: 'failed' },
      }),
    ])
    renderPage()
    await screen.findByRole('link', { name: 'weekly-digest' })
    const r = row('s1')
    expect(within(r).getByText('briefing')).toBeInTheDocument()
    expect(within(r).getByText('Weekdays, 8:00 AM')).toBeInTheDocument()
    expect(within(r).getByText('manual')).toBeInTheDocument()
    expect(within(r).getByText('Failed').closest('[data-status]')).toHaveAttribute('data-status', 'error')
  })

  it('says Never for an automation that has not run', async () => {
    vi.mocked(listAutomations).mockResolvedValue([makeAutomation()])
    renderPage()
    await screen.findByRole('link', { name: 'weekly-digest' })
    expect(within(row('s1')).getByText('Never')).toBeInTheDocument()
  })

  it('links the name to the detail page', async () => {
    vi.mocked(listAutomations).mockResolvedValue([makeAutomation()])
    const { router } = renderPage()
    fireEvent.click(await screen.findByRole('link', { name: 'weekly-digest' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/automations/s1'))
  })

  it('opens the editor from New automation', async () => {
    const { router } = renderPage()
    fireEvent.click(await screen.findByRole('button', { name: 'New automation' }))
    expect(router.state.location.pathname).toBe('/automations/new')
  })

  it('toggles enabled with a PATCH', async () => {
    vi.mocked(listAutomations).mockResolvedValue([makeAutomation()])
    vi.mocked(patchAutomation).mockResolvedValue(makeAutomation({ enabled: false }))
    renderPage()
    fireEvent.click(await screen.findByRole('switch', { name: 'weekly-digest enabled' }))
    await waitFor(() => expect(patchAutomation).toHaveBeenCalledWith('s1', { enabled: false }))
  })

  it('reverts and toasts when the toggle fails', async () => {
    vi.mocked(listAutomations).mockResolvedValue([makeAutomation()])
    vi.mocked(patchAutomation).mockRejectedValue(new Error('boom'))
    renderPage()
    fireEvent.click(await screen.findByRole('switch', { name: 'weekly-digest enabled' }))
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Could not update automation', { description: 'boom' }))
  })

  it('deletes after confirming', async () => {
    vi.mocked(listAutomations).mockResolvedValue([makeAutomation()])
    vi.mocked(deleteAutomation).mockResolvedValue()
    renderPage()
    await openMenu('weekly-digest')
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Delete' }))
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(deleteAutomation).toHaveBeenCalledWith('s1'))
    expect(toast.success).toHaveBeenCalledWith('Automation deleted')
  })

  it('cancels a delete without calling the API', async () => {
    vi.mocked(listAutomations).mockResolvedValue([makeAutomation()])
    renderPage()
    await openMenu('weekly-digest')
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Delete' }))
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
    expect(deleteAutomation).not.toHaveBeenCalled()
  })

  it('runs now from the row menu', async () => {
    vi.mocked(listAutomations).mockResolvedValue([makeAutomation()])
    vi.mocked(runAutomationNow).mockResolvedValue({ event_id: 7 })
    renderPage()
    await openMenu('weekly-digest')
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Run now' }))
    await waitFor(() => expect(runAutomationNow).toHaveBeenCalledWith('s1'))
    expect(toast.success).toHaveBeenCalledWith('Run requested')
  })

  it('opens the editor from the row menu', async () => {
    vi.mocked(listAutomations).mockResolvedValue([makeAutomation()])
    const { router } = renderPage()
    await openMenu('weekly-digest')
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Edit' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/automations/s1/edit'))
  })

  it('renders no template section when the templates endpoint 404s', async () => {
    renderPage()
    await screen.findByText('No automations yet')
    await waitFor(() => expect(listAutomationTemplates).toHaveBeenCalled())
    expect(screen.queryByText('Start from a template')).toBeNull()
    expect(toast.error).not.toHaveBeenCalled()
  })
})
