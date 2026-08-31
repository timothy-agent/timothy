import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DiscoverSection } from './DiscoverSection'

afterEach(cleanup)
beforeEach(() => {
  Object.assign(navigator, { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } })
})

describe('DiscoverSection', () => {
  it('starts collapsed, showing only the trigger', () => {
    render(<DiscoverSection notes="**bold** finding and a [link](https://example.com)" />)
    expect(screen.getByText('Show discovery')).toBeInTheDocument()
    expect(screen.queryByText('bold')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Copy discovery notes' })).not.toBeInTheDocument()
  })

  it('reveals the notes as markdown once expanded', () => {
    render(<DiscoverSection notes="**bold** finding and a [link](https://example.com)" />)
    fireEvent.click(screen.getByText('Show discovery'))

    expect(screen.getByText('bold').tagName).toBe('STRONG')
    expect(screen.getByRole('link')).toHaveAttribute('href', 'https://example.com')
  })

  it('renders a fenced code block, not a flat paragraph', () => {
    render(<DiscoverSection notes={'found the bug:\n\n```js\nconst x = 1\n```'} />)
    fireEvent.click(screen.getByText('Show discovery'))

    expect(screen.getByText('js')).toBeInTheDocument()
    expect(screen.getByText('const x = 1', { exact: false })).toBeInTheDocument()
  })

  it('has a copy button inside the content block that copies the raw notes', async () => {
    const writeText = navigator.clipboard.writeText as ReturnType<typeof vi.fn>
    render(<DiscoverSection notes="raw discovery text" />)
    fireEvent.click(screen.getByText('Show discovery'))

    fireEvent.click(screen.getByRole('button', { name: 'Copy discovery notes' }))

    expect(writeText).toHaveBeenCalledWith('raw discovery text')
  })
})
