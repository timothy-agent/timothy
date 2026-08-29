import { fireEvent, render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ResultSection } from './ResultSection'

beforeEach(() => {
  Object.assign(navigator, { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } })
})

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

  it('has a copy button that copies the raw evidence', () => {
    const writeText = navigator.clipboard.writeText as ReturnType<typeof vi.fn>
    render(<ResultSection evidence="raw result text" />)

    fireEvent.click(screen.getByRole('button', { name: 'Copy result' }))

    expect(writeText).toHaveBeenCalledWith('raw result text')
  })
})
