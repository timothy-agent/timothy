import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { Spinner } from './spinner'

afterEach(cleanup)

describe('Spinner', () => {
  it('renders a status role with an aria-label', () => {
    render(<Spinner label="Loading missions" />)
    expect(screen.getByRole('status', { name: 'Loading missions' })).toBeInTheDocument()
  })

  it('defaults the label to Loading', () => {
    render(<Spinner />)
    expect(screen.getByRole('status', { name: 'Loading' })).toBeInTheDocument()
  })
})
