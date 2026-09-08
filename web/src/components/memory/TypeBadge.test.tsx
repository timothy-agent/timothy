import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { TypeBadge } from './TypeBadge'

describe('TypeBadge', () => {
  it('renders the type text with the neutral badge variant', () => {
    render(<TypeBadge type="semantic" />)
    const badge = screen.getByText('semantic')
    expect(badge).toHaveAttribute('data-variant', 'neutral')
  })
})
