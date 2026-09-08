import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { CostDisplay } from './CostDisplay'

describe('CostDisplay', () => {
  it('renders cost only text with no budget', () => {
    render(<CostDisplay cost={0.42} currency="USD" />)
    expect(screen.getByText('$0.4200')).toBeInTheDocument()
    expect(screen.queryByRole('progressbar')).not.toBeInTheDocument()
  })

  it('renders "X of Y" text and a percentage with a budget', () => {
    render(<CostDisplay cost={0.0421} currency="USD" budget={1} />)
    expect(screen.getByText('$0.0421 of $1.00')).toBeInTheDocument()
    expect(screen.getByText('4%')).toBeInTheDocument()
    expect(screen.getByRole('progressbar', { name: 'Budget used' })).toBeInTheDocument()
  })

  it('uses brand tone under 80%', () => {
    const { container } = render(<CostDisplay cost={0.4} currency="USD" budget={1} />)
    expect(container.querySelector('[data-slot="progress-indicator"]')).toHaveClass('bg-brand')
  })

  it('uses warning tone and shows Near budget between 80 and 100%', () => {
    render(<CostDisplay cost={0.86} currency="USD" budget={1} />)
    expect(screen.getByText('Near budget')).toBeInTheDocument()
  })

  it('uses destructive tone and shows Over budget at or above 100%', () => {
    const { container } = render(<CostDisplay cost={1.2} currency="USD" budget={1} />)
    expect(screen.getByText('Over budget')).toBeInTheDocument()
    expect(container.querySelector('[data-slot="progress-indicator"]')).toHaveClass('bg-destructive')
    expect(screen.getByText('120%')).toBeInTheDocument()
  })

  it('clamps the bar value to 100%', () => {
    render(<CostDisplay cost={5} currency="USD" budget={1} />)
    const bar = screen.getByRole('progressbar', { name: 'Budget used' })
    expect(bar).toHaveAttribute('aria-valuenow', '100')
  })

  it('shows an unpriced calls line when set', () => {
    render(<CostDisplay cost={0.1} currency="USD" unpricedRequests={3} />)
    expect(screen.getByText('3 unpriced calls')).toBeInTheDocument()
  })

  it('uses singular "call" for exactly one unpriced request', () => {
    render(<CostDisplay cost={0.1} currency="USD" unpricedRequests={1} />)
    expect(screen.getByText('1 unpriced call')).toBeInTheDocument()
  })

  it('omits the unpriced line when zero', () => {
    render(<CostDisplay cost={0.1} currency="USD" unpricedRequests={0} />)
    expect(screen.queryByText(/unpriced/)).not.toBeInTheDocument()
  })

  it('renders an optional detail footnote', () => {
    render(<CostDisplay cost={0.1} currency="USD" detail="Converted from EUR at 1.08" />)
    expect(screen.getByText('Converted from EUR at 1.08')).toBeInTheDocument()
  })
})
