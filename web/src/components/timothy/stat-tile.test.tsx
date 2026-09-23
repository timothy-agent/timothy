import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { StatTile } from './stat-tile'

describe('StatTile', () => {
  it('renders the label and value', () => {
    render(<StatTile label="Total" value={12} />)
    expect(screen.getByText('Total')).toBeInTheDocument()
    expect(screen.getByText('12')).toBeInTheDocument()
  })

  it('renders an optional hint', () => {
    render(<StatTile label="Failed (7d)" value={2} hint="of 9 runs" />)
    expect(screen.getByText('of 9 runs')).toBeInTheDocument()
  })

  it('renders children below the value', () => {
    render(
      <StatTile label="Runs, 14 days" value={9}>
        <div data-testid="spark" />
      </StatTile>,
    )
    expect(screen.getByTestId('spark')).toBeInTheDocument()
  })

  it('renders no hint line when none is given', () => {
    const { container } = render(<StatTile label="Enabled" value={3} />)
    expect(container.querySelectorAll('p')).toHaveLength(2)
  })
})
