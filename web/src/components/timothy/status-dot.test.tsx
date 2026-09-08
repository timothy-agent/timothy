import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { StatusDot } from './status-dot'

afterEach(cleanup)

describe('StatusDot', () => {
  it('renders an sr-only label', () => {
    render(<StatusDot status="success" label="Done" />)
    const label = screen.getByText('Done')
    expect(label).toBeInTheDocument()
    expect(label).toHaveClass('sr-only')
  })

  it('renders the label visibly when showLabel is set', () => {
    render(<StatusDot status="working" label="Running" showLabel />)
    expect(screen.getByText('Running')).not.toHaveClass('sr-only')
  })
})
