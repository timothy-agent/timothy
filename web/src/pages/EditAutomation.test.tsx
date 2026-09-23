import { cleanup, render, screen } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Automation } from '../api/types'
import { EditAutomation } from './EditAutomation'

vi.mock('../api/client', () => ({
  getAutomation: vi.fn(),
}))

// The page's own concerns (heading, back link, not-found/loading
// states) don't need MissionForm's real dependency graph, stubbed the
// same way a page test isolates a heavy child component.
vi.mock('../components/missions/MissionForm', () => ({
  MissionForm: () => <div>mission form</div>,
}))

import { getAutomation } from '../api/client'

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

function renderAt(id: string) {
  const router = createMemoryRouter(
    [{ path: '/automations/:id/edit', element: <EditAutomation /> }],
    { initialEntries: [`/automations/${id}/edit`] },
  )
  return render(<RouterProvider router={router} />)
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getAutomation).mockRejectedValue(new Error('automation not found'))
})

describe('EditAutomation', () => {
  it('shows the "Edit automation" heading and a back link to the automation detail page', async () => {
    vi.mocked(getAutomation).mockResolvedValue(automation)
    renderAt('s1')

    expect(await screen.findByRole('heading', { name: 'Edit automation' })).toBeTruthy()
    const back = screen.getByRole('link', { name: /Automation/ })
    expect(back.getAttribute('href')).toBe('/automations/s1')
    expect(getAutomation).toHaveBeenCalledWith('s1')
  })

  it('shows a not-found message for an unknown automation', async () => {
    renderAt('missing')
    expect(await screen.findByText('Automation not found.')).toBeTruthy()
  })
})
