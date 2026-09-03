import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import type { PlanUnit } from '../../api/types'
import { PlanSection } from './PlanSection'

afterEach(cleanup)

const units: PlanUnit[] = [{ title: 'Add validation', verify_cmd: 'go test ./...', passes: true }]

describe('PlanSection', () => {
  it('renders no plan message when there are no units', () => {
    render(<PlanSection units={[]} />)
    expect(screen.getByText('No plan yet.')).toBeInTheDocument()
  })

  it('badges units by harness state: reviewed, harness-verified, pending; regressed is pending with a note', () => {
    render(
      <PlanSection
        units={[
          { title: 'done', verify_cmd: '', passes: true, harness_passed: true },
          { title: 'awaiting review', verify_cmd: '', passes: false, harness_passed: true },
          { title: 'broke', verify_cmd: '', passes: false, regressed: true, verify_check: 'artifacts', verify_excerpt: 'a.md: not found' },
          { title: 'todo', verify_cmd: '', passes: false },
        ]}
      />,
    )
    expect(screen.getByText('reviewed')).toBeInTheDocument()
    expect(screen.getByText('harness-verified')).toBeInTheDocument()
    const pending = screen.getAllByText('pending')
    expect(pending).toHaveLength(2)
    expect(pending[0]).toHaveAttribute('title', 'a.md: not found')
    expect(screen.getByText('regressed: passed before, now fails')).toBeInTheDocument()
    expect(screen.queryByText('verified')).toBeNull()
    expect(screen.queryByText('regressed')).toBeNull()
  })

  it('lists acceptance criteria under a unit and none for a legacy unit', () => {
    render(
      <PlanSection
        units={[
          { title: 'with criteria', verify_cmd: '', passes: false, criteria: ['report.md names RFC 6585', 'under 200 words'] },
          { title: 'legacy', verify_cmd: '', passes: false },
        ]}
      />,
    )
    expect(screen.getByText('report.md names RFC 6585')).toBeInTheDocument()
    expect(screen.getByText('under 200 words')).toBeInTheDocument()
    expect(screen.getByText('legacy').closest('li')?.querySelector('ul')).toBeNull()
  })

  it('does not render an Assumptions header when assumptions is absent', () => {
    render(<PlanSection units={units} />)
    expect(screen.queryByText('Assumptions')).toBeNull()
  })

  it('does not render an Assumptions header when assumptions is empty', () => {
    render(<PlanSection units={units} assumptions={[]} />)
    expect(screen.queryByText('Assumptions')).toBeNull()
  })

  it('renders assumption/default pairs when present', () => {
    render(
      <PlanSection
        units={units}
        assumptions={[{ assumption: 'no language version was specified', default: 'Python 3.12' }]}
      />,
    )
    expect(screen.getByText('Assumptions')).toBeInTheDocument()
    expect(screen.getByText(/no language version was specified/)).toBeInTheDocument()
    expect(screen.getByText(/Python 3.12/)).toBeInTheDocument()
  })
})
