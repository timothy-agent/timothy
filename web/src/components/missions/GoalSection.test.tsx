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

describe('GoalSection', () => {
  it('starts collapsed, showing a clamped preview and the trigger', () => {
    renderGoal('**bold** goal and a [link](https://example.com)')
    expect(screen.getByText('Show goal')).toBeInTheDocument()
    expect(screen.getByText('bold').tagName).toBe('STRONG')
    expect(screen.getByText('bold').closest('div')).toHaveClass('line-clamp-3')
  })

  it('renders a markdown goal formatted once expanded', () => {
    renderGoal('**bold** goal and a [link](https://example.com)')
    fireEvent.click(screen.getByText('Show goal'))

    expect(screen.getByText('bold').tagName).toBe('STRONG')
    expect(screen.getByRole('link')).toHaveAttribute('href', 'https://example.com')
  })

  it('renders a markdown list formatted once expanded', () => {
    renderGoal('Steps:\n\n- one\n- two')
    fireEvent.click(screen.getByText('Show goal'))

    expect(screen.getByRole('list')).toBeInTheDocument()
    expect(screen.getAllByRole('listitem')).toHaveLength(2)
  })

  it('renders a plain-text goal unchanged once expanded', () => {
    renderGoal('fix the login bug on the staging server')
    fireEvent.click(screen.getByText('Show goal'))

    expect(screen.getByText('fix the login bug on the staging server')).toBeInTheDocument()
  })

  it('has a copy button inside the content block that copies the raw goal', () => {
    const writeText = navigator.clipboard.writeText as ReturnType<typeof vi.fn>
    renderGoal('raw goal text')
    fireEvent.click(screen.getByText('Show goal'))

    fireEvent.click(screen.getByRole('button', { name: 'Copy goal' }))

    expect(writeText).toHaveBeenCalledWith('raw goal text')
  })
})
