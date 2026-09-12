import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NewMission } from './NewMission'

vi.mock('../api/client', () => ({ getMission: vi.fn() }))
vi.mock('../components/missions/MissionForm', () => ({
  MissionForm: ({ initialGoal }: { initialGoal?: string }) => (
    <div data-testid="seeded-goal">{initialGoal ?? ''}</div>
  ),
}))

import { getMission } from '../api/client'

afterEach(cleanup)

describe('NewMission', () => {
  it('passes a picked shortlist option through as the seeded goal', async () => {
    vi.mocked(getMission).mockResolvedValue({ id: 'parent-1', kind: 'general' } as never)
    render(
      <MemoryRouter
        initialEntries={[
          { pathname: '/missions/new', search: '?parent=parent-1', state: { pickedOptionGoal: 'Build "X".' } },
        ]}
      >
        <NewMission />
      </MemoryRouter>,
    )

    await waitFor(() => expect(screen.getByTestId('seeded-goal')).toHaveTextContent('Build "X".'))
  })

  it('seeds no goal for a plain new mission', () => {
    render(
      <MemoryRouter initialEntries={['/missions/new']}>
        <NewMission />
      </MemoryRouter>,
    )

    expect(screen.getByTestId('seeded-goal')).toHaveTextContent('')
  })
})
