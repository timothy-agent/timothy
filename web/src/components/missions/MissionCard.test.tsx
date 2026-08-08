import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'
import type { Mission } from '../../api/types'
import { MissionCard } from './MissionCard'

afterEach(cleanup)

const baseMission: Mission = {
  id: 'm1',
  goal: 'Fix the login bug on the staging server before Friday',
  kind: 'general',
  phase: 'execute',
  status: 'working',
  spec: { units: [] },
  progress: [],
  iteration: 0,
  max_iterations: 8,
  consecutive_failures: 0,
  stall_count: 0,
  route: 'default',
  review_route: 'default',
  auto_approve_safe: true,
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
