import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { Card, CardTitle } from './card'

afterEach(cleanup)

describe('Card', () => {
  it('renders a focusable link when interactive and asChild', () => {
    render(
      <Card interactive asChild>
        <a href="/missions/1">
          <CardTitle>Mission</CardTitle>
        </a>
      </Card>,
    )
    const link = screen.getByRole('link', { name: 'Mission' })
    expect(link).toHaveAttribute('href', '/missions/1')
    expect(link.className).toContain('focus-visible:ring-2')
  })

  it('applies operational density padding', () => {
    render(<Card density="operational">content</Card>)
    expect(screen.getByText('content').className).toContain('p-3')
  })
})
