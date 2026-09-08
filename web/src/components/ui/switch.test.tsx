import { cleanup, render, screen, fireEvent } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { Switch } from './switch'

afterEach(cleanup)

describe('Switch', () => {
  it('toggles aria-checked on click', () => {
    render(<Switch aria-label="Enable" />)
    const el = screen.getByRole('switch')
    expect(el).toHaveAttribute('aria-checked', 'false')
    fireEvent.click(el)
    expect(el).toHaveAttribute('aria-checked', 'true')
    fireEvent.click(el)
    expect(el).toHaveAttribute('aria-checked', 'false')
  })
})
