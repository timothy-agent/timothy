import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from './card'

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
    expect(link.className).toContain('focus-visible:border-brand')
  })

  it('applies operational density padding', () => {
    render(<Card density="operational">content</Card>)
    expect(screen.getByText('content').className).toContain('p-3')
  })

  it('renders a header with the flex column gap classes', () => {
    render(<CardHeader>header content</CardHeader>)
    const el = screen.getByText('header content')
    expect(el).toHaveAttribute('data-slot', 'card-header')
    expect(el.className).toContain('flex flex-col gap-2')
  })

  it('renders a description with muted text', () => {
    render(<CardDescription>description text</CardDescription>)
    const el = screen.getByText('description text')
    expect(el).toHaveAttribute('data-slot', 'card-description')
    expect(el.className).toContain('text-muted-foreground')
  })

  it('renders content with top margin', () => {
    render(<CardContent>body content</CardContent>)
    const el = screen.getByText('body content')
    expect(el).toHaveAttribute('data-slot', 'card-content')
    expect(el.className).toContain('mt-3')
  })

  it('renders a footer with the flex row gap classes', () => {
    render(<CardFooter>footer content</CardFooter>)
    const el = screen.getByText('footer content')
    expect(el).toHaveAttribute('data-slot', 'card-footer')
    expect(el.className).toContain('flex items-center gap-2')
  })
})
