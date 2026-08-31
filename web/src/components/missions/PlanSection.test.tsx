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
