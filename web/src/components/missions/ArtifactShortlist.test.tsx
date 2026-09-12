import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { parseOptionShortlist } from '../../lib/optionShortlist'
import { ArtifactShortlist } from './ArtifactShortlist'

const navigate = vi.fn()
vi.mock('react-router', async () => {
  const actual = await vi.importActual<typeof import('react-router')>('react-router')
  return { ...actual, useNavigate: () => navigate }
})

afterEach(() => {
  cleanup()
  navigate.mockClear()
})

function option(name: string, pitch: string): string {
  return [
    `### ${name}`,
    `**Pitch:** ${pitch}`,
    '**Fit:** satisfies R1.',
    '**Differentiation:** closest is Thing, crowded.',
    '**Build:** Go service plus a React panel.',
    '**Demo:** three beats.',
    '**Why choose it:** the judges asked for it.',
    '**Why not:** ingest may not finish.',
    '',
  ].join('\n')
}

const doc = [
  option('Signal Router', 'Routes alerts to the right human.'),
  option('Ledger Lens', 'Shows where the spend went.'),
  '## Dropped',
  '- **Prompt Wrapper:** thin wrapper.',
].join('\n')

function renderShortlist(text = doc) {
  const shortlist = parseOptionShortlist(text)!
  return render(
    <MemoryRouter>
      <ArtifactShortlist missionId="m1" shortlist={shortlist} />
    </MemoryRouter>,
  )
}

describe('ArtifactShortlist', () => {
  it('renders one collapsed row per option with name and pitch', () => {
    renderShortlist()
    expect(screen.getByText('Signal Router')).toBeTruthy()
    expect(screen.getByText('Routes alerts to the right human.')).toBeTruthy()
    expect(screen.getByText('Ledger Lens')).toBeTruthy()
    expect(screen.queryByText('three beats.')).toBeNull()
  })

  it('reveals the full section when a row is expanded', () => {
    renderShortlist()
    fireEvent.click(screen.getByText('Signal Router'))
    expect(screen.getByText('three beats.', { exact: false })).toBeTruthy()
    expect(screen.getByText('Why not', { exact: false })).toBeTruthy()
  })

  it('collapses the dropped section by default and shows entries when expanded', () => {
    renderShortlist()
    expect(screen.queryByText('thin wrapper.')).toBeNull()
    fireEvent.click(screen.getByText('Dropped (1)'))
    expect(screen.getByText('thin wrapper.')).toBeTruthy()
    expect(screen.getByText('Prompt Wrapper:')).toBeTruthy()
  })

  it('omits the dropped disclosure when the document has none', () => {
    renderShortlist(option('Only One', 'A single idea.'))
    expect(screen.queryByText('Dropped', { exact: false })).toBeNull()
  })

  it('navigates to the follow-up form with the picked option carried over', () => {
    renderShortlist()
    fireEvent.click(screen.getAllByRole('button', { name: 'Pick' })[1])
    expect(navigate).toHaveBeenCalledWith('/missions/new?parent=m1', {
      state: {
        pickedOptionGoal: [
          'Build "Ledger Lens".',
          '',
          'Objective: Shows where the spend went.',
          'Scope: Go service plus a React panel.',
        ].join('\n'),
      },
    })
  })
})
