import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { Progress } from './progress'

afterEach(cleanup)

describe('Progress', () => {
  it('renders role=progressbar with aria-valuenow', () => {
    render(<Progress value={42} aria-label="Budget used" />)
    const el = screen.getByRole('progressbar')
    expect(el).toHaveAttribute('aria-valuenow', '42')
  })

  it('treats an undefined value as 0 for the indicator transform', () => {
    render(<Progress aria-label="Budget used" />)
    const indicator = document.querySelector('[data-slot="progress-indicator"]')
    expect(indicator).toHaveStyle({ transform: 'translateX(-100%)' })
  })
})
