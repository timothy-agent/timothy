import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Schedule } from '../../api/types'
import { MissionForm } from './MissionForm'

vi.mock('../../api/client', () => ({
  createMission: vi.fn(),
  createSchedule: vi.fn(),
  patchSchedule: vi.fn(),
  listAgents: vi.fn(),
  getSettings: vi.fn(),
}))

import { createMission, createSchedule, getSettings, listAgents, patchSchedule } from '../../api/client'

const schedule: Schedule = {
  id: 's1',
  name: 'weekly-digest',
  cron: '0 8 * * 1-5',
  mission_template: {
    goal: 'Summarize the week',
    kind: 'research',
    auto_approve_safe: true,
    review_route: 'default',
  },
  enabled: true,
  expires_at: '2026-08-01T12:30:00Z',
  next_run: '2026-07-27T08:00:00Z',
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
}

afterEach(cleanup)
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listAgents).mockResolvedValue([])
  vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
})

describe('MissionForm — create mode, one-off mission', () => {
  it('submits a research mission with the entered goal', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm2' })
    const onDone = vi.fn()
    render(<MissionForm mode="create" onDone={onDone} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Research something new' } })
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

  it('allows submitting a coding mission with just a goal', async () => {
    vi.mocked(createMission).mockResolvedValue({ id: 'm3' })
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'Fix a bug' } })
    fireEvent.click(screen.getByRole('button', { name: /^Coding /i }))

    const createButton = screen.getByRole('button', { name: 'Create mission' }) as HTMLButtonElement
    expect(createButton.disabled).toBe(false)

    fireEvent.click(createButton)
    await waitFor(() =>
      expect(createMission).toHaveBeenCalledWith(expect.objectContaining({ kind: 'coding' })),
    )
  })

  it('calls onCancel when Cancel is clicked', () => {
    const onCancel = vi.fn()
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={onCancel} />)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalled()
  })
})

describe('MissionForm — create mode, repeat on schedule', () => {
  it('submits a schedule with the slugified default name, preset cron, and research kind', async () => {
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
            kind: 'research',
            auto_approve_safe: true,
          }),
        }),
      ),
    )
    expect(createMission).not.toHaveBeenCalled()
    expect(onDone).toHaveBeenCalledWith({ kind: 'schedule', id: 'sc1' })
  })

  it('disables the Coding card while repeating', () => {
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.click(screen.getByRole('button', { name: 'Repeat on schedule' }))

    const codingCard = screen.getByRole('button', { name: /^Coding /i }) as HTMLButtonElement
    expect(codingCard.disabled).toBe(true)
  })

  it('flips kind back to research when repeat turns on with coding selected', async () => {
    vi.mocked(createSchedule).mockResolvedValue({ id: 'sc1' })
    render(<MissionForm mode="create" onDone={vi.fn()} onCancel={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Goal'), { target: { value: 'g' } })
    fireEvent.click(screen.getByRole('button', { name: /^Coding /i }))
    fireEvent.click(screen.getByRole('button', { name: 'Repeat on schedule' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }))

    await waitFor(() =>
      expect(createSchedule).toHaveBeenCalledWith(
        expect.objectContaining({
          mission_template: expect.objectContaining({ kind: 'research' }),
        }),
      ),
    )
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
    fireEvent.click(screen.getByText('Show advanced options'))

    expect((screen.getByLabelText('Review route') as HTMLInputElement).value).toBe('careful')
  })
})

describe('MissionForm — edit mode', () => {
  it('prefills from the schedule', async () => {
    render(<MissionForm mode="edit" schedule={schedule} onDone={vi.fn()} onCancel={vi.fn()} />)

    expect(await screen.findByDisplayValue('weekly-digest')).toBeTruthy()
    expect(screen.getByDisplayValue('Summarize the week')).toBeTruthy()
    expect(screen.getByText('Weekdays, 8:00 AM')).toBeTruthy()
    expect((screen.getByLabelText('Expires') as HTMLInputElement).value).toBe('2026-08-01T12:30')
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
          mission_template: expect.objectContaining({ kind: 'research' }),
        }),
      ),
    )
    expect(onDone).toHaveBeenCalledWith({ kind: 'schedule', id: 's1' })
  })
})
