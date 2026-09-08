import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Mission } from '../../api/types'
import { MissionCard } from './MissionCard'

afterEach(cleanup)

const baseMission: Mission = {
  id: 'm1',
  goal: 'Fix the login bug on the staging server before Friday',
  kind: 'general',
  phase: 'generate',
  status: 'working',
  plan: { units: [] },
  progress: [],
  iteration: 0,
  max_iterations: 8,
  consecutive_failures: 0,
  stall_count: 0,
  route: 'default',
  review_route: 'default',
  auto_approve_tools: true,
  auto_approve_plan: true,
  asks_used: 0,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

function renderCard(mission: Mission) {
  return render(
    <MemoryRouter>
      <MissionCard mission={mission} />
    </MemoryRouter>,
  )
}

describe('MissionCard name fallback', () => {
  it('shows the generated name when set', () => {
    renderCard({ ...baseMission, name: 'Fix Login Bug' })
    expect(screen.getByText('Fix Login Bug')).toBeInTheDocument()
    expect(screen.queryByText(baseMission.goal)).not.toBeInTheDocument()
  })

  it('falls back to the goal when name is empty', () => {
    renderCard({ ...baseMission, name: '' })
    expect(screen.getByText(baseMission.goal)).toBeInTheDocument()
  })

  it('truncates a long goal when there is no name', () => {
    const longGoal = 'a'.repeat(80)
    renderCard({ ...baseMission, name: '', goal: longGoal })
    expect(screen.getByText(`${'a'.repeat(60)}…`)).toBeInTheDocument()
  })
})

describe('MissionCard status pill', () => {
  it('shows the raw status for a non-terminal mission', () => {
    renderCard({ ...baseMission, status: 'working', phase: 'generate' })
    expect(screen.getByText('working')).toBeInTheDocument()
  })

  it('shows "done" for a completed mission', () => {
    renderCard({ ...baseMission, status: 'done', phase: 'done' })
    expect(screen.getByText('done')).toBeInTheDocument()
  })

  it('shows "failed" for a failed mission with no failure_reason', () => {
    renderCard({ ...baseMission, status: 'error', phase: 'failed' })
    expect(screen.getByText('failed')).toBeInTheDocument()
    expect(screen.queryByText('error')).not.toBeInTheDocument()
  })

  it('shows "cancelled" instead of "failed" when failure_reason is cancelled', () => {
    renderCard({ ...baseMission, status: 'error', phase: 'failed', failure_reason: 'cancelled' })
    expect(screen.getByText('cancelled')).toBeInTheDocument()
    expect(screen.queryByText('failed')).not.toBeInTheDocument()
    expect(screen.queryByText('error')).not.toBeInTheDocument()
  })
})

describe('MissionCard harness and model', () => {
  it('shows the Native label and brand icon when harness is absent', () => {
    renderCard({ ...baseMission })
    expect(screen.getByText('Native')).toBeInTheDocument()
  })

  it('shows the pi label for harness=pi', () => {
    renderCard({ ...baseMission, harness: 'pi' })
    expect(screen.getByText('pi')).toBeInTheDocument()
  })

  it('shows the Claude Code label for harness=claude-cli', () => {
    renderCard({ ...baseMission, harness: 'claude-cli' })
    expect(screen.getByText('Claude Code')).toBeInTheDocument()
  })

  it('shows top_model when present', () => {
    renderCard({ ...baseMission, top_model: 'claude-sonnet-5' })
    expect(screen.getByText('claude-sonnet-5')).toBeInTheDocument()
  })

  it('omits model text when top_model is absent', () => {
    renderCard({ ...baseMission })
    expect(screen.queryByText('claude-sonnet-5')).not.toBeInTheDocument()
  })
})

describe('MissionCard created timestamp', () => {
  afterEach(() => vi.useRealTimers())

  it('shows the relative created time with an absolute tooltip', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-01-01T03:00:00Z'))
    renderCard({ ...baseMission, created_at: '2026-01-01T00:00:00Z' })
    const el = screen.getByText('3h ago')
    expect(el).toHaveAttribute('title', new Date('2026-01-01T00:00:00Z').toLocaleString())
  })
})

describe('MissionCard needs-approval badge', () => {
  it('shows the badge when parked on plan approval', () => {
    renderCard({ ...baseMission, phase: 'plan', pause_reason: 'approval' })
    expect(screen.getByText('needs approval')).toBeInTheDocument()
  })

  it('omits the badge for a mission not parked on plan approval', () => {
    renderCard({ ...baseMission, phase: 'plan' })
    expect(screen.queryByText('needs approval')).not.toBeInTheDocument()
  })
})

describe('MissionCard needs-answer badge', () => {
  it('shows the badge when pending_input is set', () => {
    renderCard({
      ...baseMission,
      pending_input: {
        question: 'Which environment?',
        kind: 'open',
        proposed_default: 'staging',
        asked_at: '2026-01-01T00:00:00Z',
        phase: 'generate',
      },
    })
    expect(screen.getByText('needs answer')).toBeInTheDocument()
  })

  it('omits the badge when pending_input is unset', () => {
    renderCard({ ...baseMission })
    expect(screen.queryByText('needs answer')).not.toBeInTheDocument()
  })
})

describe('MissionCard phase step text', () => {
  it('shows the phase step for a non-terminal mission', () => {
    renderCard({ ...baseMission, phase: 'generate' })
    expect(screen.getByText('Generate · 3 of 5')).toBeInTheDocument()
  })

  it('shows Done for a completed mission', () => {
    renderCard({ ...baseMission, phase: 'done', status: 'done' })
    expect(screen.getByText('Done')).toBeInTheDocument()
  })
})

describe('MissionCard next run', () => {
  it('shows the next run metadata item when given one', () => {
    renderCard({ ...baseMission })
    render(
      <MemoryRouter>
        <MissionCard mission={baseMission} nextRun="in 2h" />
      </MemoryRouter>,
    )
    expect(screen.getByText('Next run in 2h')).toBeInTheDocument()
  })

  it('omits the next run item when not given one', () => {
    renderCard({ ...baseMission })
    expect(screen.queryByText(/Next run/)).not.toBeInTheDocument()
  })
})

describe('MissionCard cost', () => {
  it('shows the cost when given one', () => {
    renderCard({ ...baseMission })
    render(
      <MemoryRouter>
        <MissionCard mission={baseMission} cost="$0.0421" />
      </MemoryRouter>,
    )
    expect(screen.getByText('$0.0421')).toBeInTheDocument()
  })
})

describe('MissionCard recurring badge', () => {
  it('shows a recurring badge when schedule_id is set', () => {
    renderCard({ ...baseMission, schedule_id: 's1' })
    expect(screen.getByText('recurring')).toBeInTheDocument()
  })

  it('omits the recurring badge when schedule_id is unset', () => {
    renderCard({ ...baseMission })
    expect(screen.queryByText('recurring')).not.toBeInTheDocument()
  })
})

describe('MissionCard pause message', () => {
  it('shows the pause message when set', () => {
    renderCard({ ...baseMission, pause_message: 'Waiting on budget approval' })
    expect(screen.getByText('Waiting on budget approval')).toBeInTheDocument()
  })

  it('omits the pause message when unset', () => {
    renderCard({ ...baseMission })
    expect(screen.queryByText('Waiting on budget approval')).not.toBeInTheDocument()
  })
})

describe('MissionCard removed fields', () => {
  it('never renders retries, unit progress, or the raw phase text', () => {
    renderCard({
      ...baseMission,
      phase: 'generate',
      iteration: 3,
      plan: { units: [{ title: 'a', verify_cmd: '', passes: true }, { title: 'b', verify_cmd: '', passes: false }] },
    })
    expect(screen.queryByText(/Retries/)).not.toBeInTheDocument()
    expect(screen.queryByText(/\d+\/\d+ units/)).not.toBeInTheDocument()
    expect(screen.queryByText('generate')).not.toBeInTheDocument()
  })
})
