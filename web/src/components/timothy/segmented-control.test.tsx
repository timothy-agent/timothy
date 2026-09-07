import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { SegmentedControl } from './segmented-control'

const options = [
  { value: 'day', label: 'Day' },
  { value: 'week', label: 'Week' },
]

function Harness() {
  const [value, setValue] = useState('day')
  return <SegmentedControl value={value} onChange={setValue} options={options} aria-label="Range" />
}

describe('SegmentedControl', () => {
  it('renders a group with items reflecting selection state', () => {
    render(<Harness />)
    expect(screen.getByRole('radiogroup', { name: 'Range' })).toBeInTheDocument()
    expect(screen.getByRole('radio', { name: 'Day' })).toHaveAttribute('data-state', 'on')
    expect(screen.getByRole('radio', { name: 'Week' })).toHaveAttribute('data-state', 'off')
  })

  it('switches selection on click', () => {
    render(<Harness />)
    fireEvent.click(screen.getByRole('radio', { name: 'Week' }))
    expect(screen.getByRole('radio', { name: 'Week' })).toHaveAttribute('data-state', 'on')
  })

  it('ignores an attempt to deselect the active item', () => {
    render(<Harness />)
    fireEvent.click(screen.getByRole('radio', { name: 'Day' }))
    expect(screen.getByRole('radio', { name: 'Day' })).toHaveAttribute('data-state', 'on')
  })
})
