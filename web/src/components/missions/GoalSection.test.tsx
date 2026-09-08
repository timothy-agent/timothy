import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '../ui/tooltip'
import { GoalSection } from './GoalSection'

afterEach(cleanup)
beforeEach(() => {
  Object.assign(navigator, { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } })
})

function renderGoal(goal: string) {
  return render(
    <TooltipProvider>
      <GoalSection goal={goal} />
    </TooltipProvider>,
  )
}

// The section ships collapsed; every body assertion expands it first.
function renderExpandedGoal(goal: string) {
  const r = renderGoal(goal)
  fireEvent.click(screen.getByRole('button', { name: 'Show goal' }))
  return r
}

describe('GoalSection', () => {
  it('starts collapsed and reveals the body on toggle', () => {
    renderGoal('**bold** goal and a [link](https://example.com)')
    expect(screen.queryByText('bold')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Show goal' }))
    expect(screen.getByText('bold').tagName).toBe('STRONG')
    fireEvent.click(screen.getByRole('button', { name: 'Hide goal' }))
    expect(screen.queryByText('bold')).not.toBeInTheDocument()
  })

  it('renders a markdown goal formatted once expanded', () => {
    renderExpandedGoal('**bold** goal and a [link](https://example.com)')

    expect(screen.getByText('bold').tagName).toBe('STRONG')
    expect(screen.getByRole('link')).toHaveAttribute('href', 'https://example.com')
  })

  it('renders a markdown list formatted once expanded', () => {
    renderExpandedGoal('Steps:\n\n- one\n- two')

    expect(screen.getByRole('list')).toBeInTheDocument()
    expect(screen.getAllByRole('listitem')).toHaveLength(2)
  })

  it('renders a plain-text goal unchanged once expanded', () => {
    renderExpandedGoal('fix the login bug on the staging server')

    expect(screen.getByText('fix the login bug on the staging server')).toBeInTheDocument()
  })

  it('has a copy button inside the content block that copies the raw goal', () => {
    const writeText = navigator.clipboard.writeText as ReturnType<typeof vi.fn>
    renderExpandedGoal('raw goal text')
    fireEvent.click(screen.getByRole('button', { name: 'Copy goal' }))

    expect(writeText).toHaveBeenCalledWith('raw goal text')
  })
})
