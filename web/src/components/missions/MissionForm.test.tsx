import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminRoute, Schedule } from '../../api/types'
import { MissionForm } from './MissionForm'

vi.mock('../../api/client', () => ({
  classifyMission: vi.fn(),
  createMission: vi.fn(),
  createSchedule: vi.fn(),
  patchSchedule: vi.fn(),
  listAgents: vi.fn(),
  listRoutes: vi.fn(),
  getSettings: vi.fn(),
  getMissionExecutorOptions: vi.fn(),
}))

import {
  classifyMission,
  createMission,
  createSchedule,
  getMissionExecutorOptions,
  getSettings,
  listAgents,
  listRoutes,
  patchSchedule,
} from '../../api/client'

const routes: AdminRoute[] = [
  { name: 'default', strategy: 'ordered', enabled: true, chain: [] },
  { name: 'careful', strategy: 'ordered', enabled: true, chain: [] },
  { name: 'disabled-route', strategy: 'ordered', enabled: false, chain: [] },
]

const schedule: Schedule = {
  id: 's1',
  name: 'weekly-digest',
  cron: '0 8 * * 1-5',
  mission_template: {
    goal: 'Summarize the week',
    kind: 'general',
    auto_approve_safe: true,
    review_route: 'default',
  },
  enabled: true,
  expires_at: '2026-08-01T12:30:00Z',
  next_run: '2026-07-27T08:00:00Z',
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  pending_fire: false,
}

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listAgents).mockResolvedValue([])
  vi.mocked(listRoutes).mockResolvedValue(routes)
  vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
  vi.mocked(classifyMission).mockResolvedValue({ kind: 'general' })
  vi.mocked(getMissionExecutorOptions).mockResolvedValue([])
})

describe('MissionForm — create mode, one-off mission', () => {
  it('submits a general mission with the entered goal', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm2' })
    const onDone = vi.fn()
    render(<MissionForm mode="create" onDone={onDone} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Research something new' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({
          goal: 'Research something new',
          kind: 'general',
          auto_approve_safe: true,
        }),
      ),
    )
    expect(onDone).toHaveBeenCalledWith({ kind: 'mission', id: 'm2' })
  })

  it('sends auto_approve_safe: false when the toggle is unchecked', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm2' })
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Research something new' } })
    fireEvent.click(screen.getByRole('button', { name: 'Show advanced options' }))
    fireEvent.click(screen.getByLabelText(/Auto-approve safe tool calls/))
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(
        expect.objectContaining({ auto_approve_safe: false }),
      ),
    )
  })

  it('disables submit until a goal is entered', () => {
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    const createButton = screen.getByRole('button', { name: 'Create mission' }) as HTMLButtonElement
    expect(createButton.disabled).toBe(true)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug' } })
    expect(createButton.disabled).toBe(false)
  })

  it('calls onCancel when Cancel is clicked', () => {
    const onCancel = vi.fn()
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={onCancel} />)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalled()
  })
})

describe('MissionForm — kind chip', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows a detecting state then the classified kind after the debounce', async () => {
    vi.mocked(classifyMission).mockResolvedValue({ kind: 'coding' })
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug in the repo' } })
    expect(screen.getByText('Detecting…')).toBeInTheDocument()
    expect(classifyMission).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(600)
    await vi.advanceTimersByTimeAsync(0)

    expect(classifyMission).toHaveBeenCalledWith('Fix a bug in the repo')
    expect(screen.getByText('Coding · branches from repo')).toBeInTheDocument()
  })

  it('debounces repeated goal edits into a single classify call', async () => {
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    const goalInput = screen.getByLabelText('Goal')
    fireEvent.change(goalInput, { target: { value: 'Fix a' } })
    await vi.advanceTimersByTimeAsync(300)
    fireEvent.change(goalInput, { target: { value: 'Fix a bug' } })
    await vi.advanceTimersByTimeAsync(600)

    expect(classifyMission).toHaveBeenCalledTimes(1)
    expect(classifyMission).toHaveBeenCalledWith('Fix a bug')
  })

  it('submits the mission with the classified kind', async () => {
    vi.mocked(classifyMission).mockResolvedValue({ kind: 'coding' })
    vi.mocked(createMission).mockResolvedValue({ id: 'm3' })
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug' } })
    await vi.advanceTimersByTimeAsync(600)
    await vi.advanceTimersByTimeAsync(0)

    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ kind: 'coding' }))
  })

  it('clicking the chip toggles kind and locks it against further auto-detect', async () => {
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug' } })
    await vi.advanceTimersByTimeAsync(600)
    await vi.advanceTimersByTimeAsync(0)
    expect(screen.getByText('General · scratch workspace')).toBeInTheDocument()

    fireEvent.click(screen.getByText('General · scratch workspace'))
    expect(screen.getByText('Coding · branches from repo')).toBeInTheDocument()

    vi.mocked(classifyMission).mockClear()
    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug in the app' } })
    await vi.advanceTimersByTimeAsync(600)
    await vi.advanceTimersByTimeAsync(0)

    // Locked: the further edit above must not trigger a reclassify.
    expect(classifyMission).not.toHaveBeenCalled()
    expect(screen.getByText('Coding · branches from repo')).toBeInTheDocument()
  })
})

describe('MissionForm — executor select placement', () => {
  it('shows the executor select in the main form body for a coding mission, without expanding Advanced', async () => {
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(await screen.findByText('General · scratch workspace'))
    expect(screen.getByText('Coding · branches from repo')).toBeInTheDocument()

    expect(screen.getByLabelText('Executor')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Hide advanced options' })).toBeNull()
  })

  it('omits the executor select for a general mission', async () => {
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    expect(await screen.findByText('General · scratch workspace')).toBeInTheDocument()
    expect(screen.queryByLabelText('Executor')).toBeNull()
  })

  it('submits the picked executor for a coding mission', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm5' })
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(await screen.findByText('General · scratch workspace'))
    fireEvent.click(screen.getByLabelText('Executor'))
    fireEvent.click(await screen.findByText('Claude Code'))
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ harness: 'claude-cli' })),
    )
  })
})

describe('MissionForm — environment select', () => {
  it('shows the environment select for a coding mission, defaulted to Auto-detect', async () => {
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(await screen.findByText('General · scratch workspace'))

    expect(screen.getByLabelText('Environment')).toBeInTheDocument()
    expect(screen.getByLabelText('Environment')).toHaveTextContent('Auto-detect')
  })

  it('omits the environment select for a general mission', async () => {
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    expect(await screen.findByText('General · scratch workspace')).toBeInTheDocument()
    expect(screen.queryByLabelText('Environment')).toBeNull()
  })

  it('submits the picked environment for a coding mission, and omits it when left on Auto-detect', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm6' })
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(await screen.findByText('General · scratch workspace'))
    fireEvent.click(screen.getByLabelText('Environment'))
    fireEvent.click(await screen.findByText('Go'))
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ environment: 'go' })),
    )
  })

  it('omits environment from the create payload when left on Auto-detect', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm7' })
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(await screen.findByText('General · scratch workspace'))
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ environment: undefined })),
    )
  })
})

describe('MissionForm — create mode, repeat on schedule', () => {
  it('submits a schedule with the slugified default name, preset cron, and general kind', async () => {
    vi.mocked(createSchedule).mockResolvedValue({ id: 'sc1' })
    const onDone = vi.fn()
    render(<MissionForm mode="create" onDone={onDone} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), {
      target: { value: 'Check the news every morning' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Repeat on schedule' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }))

    await waitFor(() =>
      expect(createSchedule).toHaveBeenCalledWith(
        expect.objectContaining({
          name: 'check-the-news-every-morning',
          cron: '0 7 * * *',
          mission_template: expect.objectContaining({
            goal: 'Check the news every morning',
            kind: 'general',
            auto_approve_safe: true,
          }),
        }),
      ),
    )
    expect(createMission).not.toHaveBeenCalled()
    expect(onDone).toHaveBeenCalledWith({ kind: 'schedule', id: 'sc1' })
  })

  it('forces kind to general and locks it when repeat turns on with coding selected', async () => {
    vi.useFakeTimers()
    vi.mocked(classifyMission).mockResolvedValue({ kind: 'coding' })
    vi.mocked(createSchedule).mockResolvedValue({ id: 'sc1' })
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    await vi.advanceTimersByTimeAsync(600)
    await vi.advanceTimersByTimeAsync(0)
    expect(screen.getByText('Coding · branches from repo')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Repeat on schedule' }))
    expect(screen.getByText('General · scratch workspace')).toBeInTheDocument()

    vi.useRealTimers()
    fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }))

    await waitFor(() =>
      expect(createSchedule).toHaveBeenCalledWith(
        expect.objectContaining({
          mission_template: expect.objectContaining({ kind: 'general' }),
        }),
      ),
    )
  })

  it('disables the chip toggle while repeating (coding unavailable)', async () => {
    vi.useFakeTimers()
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(screen.getByRole('button', { name: 'Repeat on schedule' }))
    await vi.advanceTimersByTimeAsync(600)
    await vi.advanceTimersByTimeAsync(0)

    expect(screen.getByText('General · scratch workspace')).toBeInTheDocument()
    fireEvent.click(screen.getByText('General · scratch workspace'))
    // Still general: the chip toggle no-ops for coding while repeating.
    expect(screen.getByText('General · scratch workspace')).toBeInTheDocument()
    vi.useRealTimers()
  })

  it('blocks submit on a malformed custom cron shape', async () => {
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(screen.getByRole('button', { name: 'Repeat on schedule' }))
    fireEvent.click(screen.getByRole('combobox'))
    fireEvent.click(await screen.findByText('Custom'))
    fireEvent.change(screen.getByLabelText('Cron expression'), { target: { value: 'bad cron' } })

    const submitButton = screen.getByRole('button', { name: 'Create schedule' }) as HTMLButtonElement
    expect(submitButton.disabled).toBe(true)
    expect(createSchedule).not.toHaveBeenCalled()
  })

  it('cascades the picked agent onto review route the same as a one-off mission', async () => {
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
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(await screen.findByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(screen.getByRole('button', { name: 'Repeat on schedule' }))

    // Combobox order while repeating: Runs (cron preset), then Agent.
    fireEvent.click(screen.getAllByRole('combobox')[1])
    fireEvent.click(await screen.findByText('briefing'))
    fireEvent.click(screen.getByRole('button', { name: 'Show advanced options' }))

    expect(screen.getByLabelText('Review route')).toHaveTextContent('careful')
  })

  it('renders route selects fed from the live routes list, excluding disabled routes', async () => {
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(await screen.findByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(screen.getByRole('button', { name: 'Show advanced options' }))
    await screen.findByLabelText('Review route')

    fireEvent.click(screen.getByLabelText('Route'))
    expect(await screen.findByText('careful')).toBeInTheDocument()
    expect(screen.queryByText('disabled-route')).not.toBeInTheDocument()
  })

  it('submits the picked route and review route', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm4' })
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug' } })
    fireEvent.click(screen.getByRole('button', { name: 'Show advanced options' }))
    await screen.findByLabelText('Review route')

    fireEvent.click(screen.getByLabelText('Route'))
    fireEvent.click(await screen.findByText('careful'))
    fireEvent.click(screen.getByRole('button', { name: 'Create mission' }))

    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ route: 'careful' })),
    )
  })
})

describe('MissionForm — edit mode', () => {
  it('prefills from the schedule, chip locked to the template kind', async () => {
    render(<MissionForm mode="edit" schedule={schedule} onDone={vi.fn()} onCancel={vi.fn()} />)

    expect(await screen.findByDisplayValue('weekly-digest')).toBeTruthy()
    expect(screen.getByDisplayValue('Summarize the week')).toBeTruthy()
    expect(screen.getByText('Weekdays, 8:00 AM')).toBeTruthy()
    expect(screen.getByLabelText('Expires')).toHaveTextContent('Aug 1, 2026, 12:30')
    expect(screen.getByText('General · scratch workspace')).toBeInTheDocument()
    expect(classifyMission).not.toHaveBeenCalled()
  })

  it('auto-expands Advanced when the schedule has a non-default review route', async () => {
    render(<MissionForm mode="edit" schedule={schedule} onDone={vi.fn()} onCancel={vi.fn()} />)

    await screen.findByDisplayValue('weekly-digest')
    expect(screen.getByLabelText('Review route')).toBeInTheDocument()
  })

  it('preserves the schedule kind in the patch payload and never shows Run once', async () => {
    vi.mocked(patchSchedule).mockResolvedValue(schedule)
    const onDone = vi.fn()
    render(<MissionForm mode="edit" schedule={schedule} onDone={onDone} onCancel={vi.fn()} />)

    expect(screen.queryByRole('button', { name: 'Run once' })).toBeNull()

    await screen.findByDisplayValue('weekly-digest')
    fireEvent.click(screen.getByRole('button', { name: 'Save schedule' }))

    await waitFor(() =>
      expect(patchSchedule).toHaveBeenCalledWith(
        's1',
        expect.objectContaining({
          name: 'weekly-digest',
          mission_template: expect.objectContaining({ kind: 'general' }),
        }),
      ),
    )
    expect(onDone).toHaveBeenCalledWith({ kind: 'schedule', id: 's1' })
  })

  it('picks a new expiry date from the calendar and submits it', async () => {
    vi.mocked(patchSchedule).mockResolvedValue(schedule)
    render(<MissionForm mode="edit" schedule={schedule} onDone={vi.fn()} onCancel={vi.fn()} />)

    await screen.findByDisplayValue('weekly-digest')
    fireEvent.click(screen.getByLabelText('Expires'))
    fireEvent.click(await screen.findByRole('button', { name: /August 15th, 2026/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Save schedule' }))

    await waitFor(() =>
      expect(patchSchedule).toHaveBeenCalledWith(
        's1',
        expect.objectContaining({
          expires_at: expect.stringContaining('-15T12:30'),
        }),
      ),
    )
  })

  it('clears the expiry back to never', async () => {
    vi.mocked(patchSchedule).mockResolvedValue(schedule)
    render(<MissionForm mode="edit" schedule={schedule} onDone={vi.fn()} onCancel={vi.fn()} />)

    await screen.findByDisplayValue('weekly-digest')
    fireEvent.click(screen.getByLabelText('Expires'))
    fireEvent.click(await screen.findByRole('button', { name: 'Clear' }))
    expect(screen.getByLabelText('Expires')).toHaveTextContent('Never')

    fireEvent.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() =>
      expect(patchSchedule).toHaveBeenCalledWith(
        's1',
        expect.objectContaining({ expires_at: null }),
      ),
    )
  })
})
