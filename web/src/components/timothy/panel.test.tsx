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

  it('applies bodyClassName to the body wrapper, keeping the padding', () => {
    render(
      <Panel title="Chart" bodyClassName="flex flex-1 flex-col">
        <div>plot</div>
      </Panel>,
    )
    const body = screen.getByText('plot').parentElement
    expect(body).toHaveClass('flex', 'flex-1', 'flex-col', 'p-5', 'pt-0')
  })

  it('renders no body and no bottom padding when children are false', () => {
    render(
      <Panel title="Goal" actions={<button type="button">Show goal</button>}>
        {false}
      </Panel>,
    )
    const header = screen.getByRole('heading', { name: 'Goal' }).closest('[data-density]')?.firstElementChild
    expect(header).toHaveClass('p-5')
    expect(header).not.toHaveClass('pb-0')
    expect(header?.firstElementChild).not.toHaveClass('mb-4')
    expect(header?.nextElementSibling).toBeNull()
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

  it('renders a leading control before the title', () => {
    render(
      <Panel title="Files" leading={<button type="button">Toggle</button>}>
        body
      </Panel>,
    )
    const toggle = screen.getByRole('button', { name: 'Toggle' })
    const title = screen.getByText('Files')
    expect(toggle.compareDocumentPosition(title) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})
