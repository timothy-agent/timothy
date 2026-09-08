import { cleanup, render, screen, fireEvent } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { Checkbox } from './checkbox'

afterEach(cleanup)

describe('Checkbox', () => {
  it('toggles aria-checked on click', () => {
    render(<Checkbox aria-label="Accept" />)
    const el = screen.getByRole('checkbox')
    expect(el).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(el)
    expect(el).toHaveAttribute('aria-checked', 'true')
  })

  it('renders indeterminate state', () => {
    render(<Checkbox aria-label="Select all" checked="indeterminate" />)
    const el = screen.getByRole('checkbox')
    expect(el).toHaveAttribute('aria-checked', 'mixed')
  })
})
