import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Panel } from './panel'

describe('Panel', () => {
  it('defaults to comfortable density', () => {
    render(<Panel title="Goal">Body</Panel>)
    expect(screen.getByText('Body').closest('[data-density]')).toHaveAttribute('data-density', 'comfortable')
  })

  it('sets operational density and renders header + body', () => {
    render(
      <Panel title="Timeline" density="operational">
        <div>row</div>
      </Panel>,
    )
    expect(screen.getByText('row').closest('[data-density]')).toHaveAttribute('data-density', 'operational')
    expect(screen.getByRole('heading', { name: 'Timeline' })).toBeInTheDocument()
  })

  it('defaults the title to an h2', () => {
    render(<Panel title="Goal">Body</Panel>)
    const heading = screen.getByRole('heading', { name: 'Goal', level: 2 })
    expect(heading.tagName).toBe('H2')
    expect(heading).toHaveClass('text-sm', 'leading-5', 'font-semibold')
  })

  it('renders the title as h3 when nested under a section heading', () => {
    render(
      <Panel title="Goal" headingLevel="h3">
        Body
      </Panel>,
    )
    const heading = screen.getByRole('heading', { name: 'Goal', level: 3 })
    expect(heading.tagName).toBe('H3')
    expect(heading).toHaveClass('text-sm', 'leading-5', 'font-semibold')
  })

  it('renders description and actions', () => {
    render(
      <Panel title="Goal" description="What Timothy is trying to do" actions={<button>Edit</button>}>
        Body
      </Panel>,
    )
    expect(screen.getByText('What Timothy is trying to do')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Edit' })).toBeInTheDocument()
  })
})
