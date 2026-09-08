import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { StatusBadge } from './status-badge'

afterEach(cleanup)

describe('StatusBadge', () => {
  it('renders the default label and data-status', () => {
    render(<StatusBadge status="working" />)
    expect(screen.getByText('Working')).toBeInTheDocument()
    expect(screen.getByText('Working').closest('span')).toHaveAttribute('data-status', 'working')
  })

  it('renders a custom label', () => {
    render(<StatusBadge status="error" label="Failed" />)
    expect(screen.getByText('Failed')).toBeInTheDocument()
  })
})
