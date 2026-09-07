import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ResultSection } from './ResultSection'

describe('ResultSection', () => {
  it('renders the evidence text as markdown', () => {
    render(<ResultSection evidence={'**bold** and a [link](https://example.com)'} />)
    expect(screen.getByRole('strong')).toHaveTextContent('bold')
    expect(screen.getByRole('link')).toHaveAttribute('href', 'https://example.com')
  })

  it('renders a fenced code block, not a flat paragraph', () => {
    render(<ResultSection evidence={'result:\n\n```js\nconst x = 1\n```'} />)
    expect(screen.getByText('js')).toBeInTheDocument()
    expect(screen.getByText('const x = 1', { exact: false })).toBeInTheDocument()
  })

  it('shows a "Show all" toggle that removes the clamp', () => {
    render(<ResultSection evidence="raw result text" />)
    const toggle = screen.getByRole('button', { name: 'Show all' })
    fireEvent.click(toggle)
    expect(screen.getByRole('button', { name: 'Show less' })).toBeInTheDocument()
  })
})
