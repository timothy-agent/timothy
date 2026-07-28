import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Mission, Notification } from '../api/types'
import { Missions } from './Missions'

vi.mock('../api/client', () => ({
  listMissions: vi.fn(),
  listNotifications: vi.fn(),
  markNotificationRead: vi.fn(),
  createMission: vi.fn(),
  createSchedule: vi.fn(),
  listSchedules: vi.fn(),
  listAgents: vi.fn(),
}))

import {
  createMission,
  createSchedule,
  listAgents,
  listMissions,
  listNotifications,
  listSchedules,
} from '../api/client'

const mission: Mission = {
  id: 'm1',
  goal: 'Fix the login bug',
  kind: 'research',
  phase: 'execute',
  status: 'working',
  spec: { units: [] },
  progress: [],
  iteration: 1,
  max_iterations: 8,
  consecutive_failures: 0,
  stall_count: 0,
  route: 'default',
  review_route: 'default',
  auto_approve_safe: true,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

function renderPage() {
  return render(
    <MemoryRouter>
      <Missions />
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listMissions).mockResolvedValue([mission])
  vi.mocked(listNotifications).mockResolvedValue([])
  vi.mocked(listAgents).mockResolvedValue([])
  vi.mocked(listSchedules).mockResolvedValue([])
})

describe('Missions board', () => {
  it('renders the mission card list', async () => {
    renderPage()
    expect(await screen.findByText('Fix the login bug')).toBeTruthy()
    expect(screen.getByText('working')).toBeTruthy()
  })

  it('shows an unread notification strip', async () => {
    const note: Notification = {
      id: 'n1',
      mission_id: 'm1',
      kind: 'paused',
      message: 'mission m1 is now paused',
      read: false,
      created_at: '2026-01-01T00:00:00Z',
    }
    vi.mocked(listNotifications).mockResolvedValue([note])
    renderPage()
    expect(await screen.findByText('mission m1 is now paused')).toBeTruthy()
  })

  it('does not show read notifications', async () => {
    const note: Notification = {
      id: 'n1',
      mission_id: 'm1',
      kind: 'done',
      message: 'mission m1 is now done',
      read: true,
      created_at: '2026-01-01T00:00:00Z',
    }
    vi.mocked(listNotifications).mockResolvedValue([note])
    renderPage()
    await waitFor(() => expect(listNotifications).toHaveBeenCalled())
    expect(screen.queryByText('mission m1 is now done')).toBeNull()
  })

  it('opens the new mission dialog and submits a research mission', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm2' })
    renderPage()
    await screen.findByText('Fix the login bug')

    fireEvent.click(screen.getByRole('button', { name: 'New mission' }))
    const goalField = await screen.findByLabelText('Goal')
    fireEvent.change(goalField, { target: { value: 'Research something new' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({
          goal: 'Research something new',
          kind: 'research',
          auto_approve_safe: true,
        }),
      ),
    )
  })

  it('sends auto_approve_safe: false when the toggle is unchecked', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm2' })
    renderPage()
    await screen.findByText('Fix the login bug')

    fireEvent.click(screen.getByRole('button', { name: 'New mission' }))
    const goalField = await screen.findByLabelText('Goal')
    fireEvent.change(goalField, { target: { value: 'Research something new' } })
    fireEvent.click(screen.getByRole('button', { name: 'Show advanced options' }))
    fireEvent.click(screen.getByLabelText(/Auto-approve safe tool calls/))
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({ auto_approve_safe: false }),
      ),
    )
  })

  it('disables submit until a goal is entered', async () => {
    renderPage()
    await screen.findByText('Fix the login bug')
    fireEvent.click(screen.getByRole('button', { name: 'New mission' }))
    await screen.findByLabelText('Goal')

    const createButton = screen.getByRole('button', { name: 'Create mission' }) as HTMLButtonElement
    expect(createButton.disabled).toBe(true)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug' } })
    expect(createButton.disabled).toBe(false)
  })

  it('requires a repo path before submitting a coding mission', async () => {
    // jsdom lacks scrollIntoView; Radix Select calls it on open.
    Element.prototype.scrollIntoView = vi.fn()
    renderPage()
    await screen.findByText('Fix the login bug')
    fireEvent.click(screen.getByRole('button', { name: 'New mission' }))
    await screen.findByLabelText('Goal')

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug' } })
    fireEvent.click(screen.getByRole('combobox'))
    fireEvent.click(await screen.findByText('Coding'))

    const createButton = screen.getByRole('button', { name: 'Create mission' }) as HTMLButtonElement
    expect(createButton.disabled).toBe(true)

    fireEvent.change(screen.getByLabelText('Repository path'), { target: { value: '/workspace/repo' } })
    expect(createButton.disabled).toBe(false)
  })
})

describe('Missions board — repeat on schedule', () => {
  it('submits a schedule with the slugified default name, preset cron, and research kind', async () => {
    vi.mocked(createSchedule).mockResolvedValue({ id: 'sc1' })
    renderPage()
    await screen.findByText('Fix the login bug')

    fireEvent.click(screen.getByRole('button', { name: 'New mission' }))
    fireEvent.change(await screen.findByLabelText('Goal'), {
      target: { value: 'Check the news every morning' },
    })
    fireEvent.click(screen.getByLabelText(/Repeat on schedule/))
    fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }))

    await waitFor(() =>
      expect(createSchedule).toHaveBeenCalledWith(
        expect.objectContaining({
          name: 'check-the-news-every-morning',
          cron: '0 7 * * *',
          mission_template: expect.objectContaining({
            goal: 'Check the news every morning',
            kind: 'research',
            auto_approve_safe: true,
          }),
        }),
      ),
    )
    expect(createMission).not.toHaveBeenCalled()
  })

  it('disables the Coding kind while repeating', async () => {
    Element.prototype.scrollIntoView = vi.fn()
    renderPage()
    await screen.findByText('Fix the login bug')

    fireEvent.click(screen.getByRole('button', { name: 'New mission' }))
    await screen.findByLabelText('Goal')
    fireEvent.click(screen.getByLabelText(/Repeat on schedule/))
    fireEvent.click(screen.getAllByRole('combobox')[0])

    expect((await screen.findByText('Coding')).closest('[role="option"]')).toHaveAttribute(
      'data-disabled',
    )
  })

  it('cascades the picked agent onto review route the same as a one-off mission', async () => {
    Element.prototype.scrollIntoView = vi.fn()
    vi.mocked(listAgents).mockResolvedValue([
      {
        id: 'a1',
        name: 'briefing',
        description: '',
        prompt_overlay: '',
        route: '',
        skills: [],
        tools: [],
        memory: false,
        is_default: false,
        enabled: true,
        review_route: 'careful',
      },
    ])
    vi.mocked(createSchedule).mockResolvedValue({ id: 'sc1' })
    renderPage()
    await screen.findByText('Fix the login bug')

    fireEvent.click(screen.getByRole('button', { name: 'New mission' }))
    fireEvent.change(await screen.findByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(screen.getByLabelText(/Repeat on schedule/))

    // Combobox order: Kind, Agent, Runs (cron preset) — the agent
    // picker is the second one once repeat is on.
    fireEvent.click(screen.getAllByRole('combobox')[1])
    fireEvent.click(await screen.findByText('briefing'))
    fireEvent.click(screen.getByText('Show advanced options'))

    expect((screen.getByLabelText('Review route') as HTMLInputElement).value).toBe('careful')
  })
})
