import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '../ui/tooltip'
import { DiscoverSection } from './DiscoverSection'

afterEach(cleanup)
beforeEach(() => {
  Object.assign(navigator, { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } })
})

function renderNotes(notes: string) {
  return render(
    <TooltipProvider>
      <DiscoverSection notes={notes} />
    </TooltipProvider>,
  )
}

// The section ships collapsed; every body assertion expands it first.
function renderExpandedNotes(notes: string) {
  const r = renderNotes(notes)
  fireEvent.click(screen.getByRole('button', { name: 'Show discovery' }))
  return r
}

describe('DiscoverSection', () => {
  it('starts collapsed and reveals the body and copy action on toggle', () => {
    renderNotes('**bold** finding and a [link](https://example.com)')
    expect(screen.queryByText('bold')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Copy discovery notes' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Show discovery' }))
    expect(screen.getByText('bold').tagName).toBe('STRONG')
    expect(screen.getByRole('button', { name: 'Copy discovery notes' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Hide discovery' }))
    expect(screen.queryByText('bold')).not.toBeInTheDocument()
  })

  it('reveals the notes as markdown once expanded', () => {
    renderExpandedNotes('**bold** finding and a [link](https://example.com)')
    expect(screen.getByText('bold').tagName).toBe('STRONG')
    expect(screen.getByRole('link')).toHaveAttribute('href', 'https://example.com')
  })

  it('renders a fenced code block, not a flat paragraph', () => {
    renderExpandedNotes('found the bug:\n\n```js\nconst x = 1\n```')
    expect(screen.getByText('js')).toBeInTheDocument()
    expect(screen.getByText('const x = 1', { exact: false })).toBeInTheDocument()
  })

  it('has a copy button inside the content block that copies the raw notes', async () => {
    const writeText = navigator.clipboard.writeText as ReturnType<typeof vi.fn>
    renderExpandedNotes('raw discovery text')
    fireEvent.click(screen.getByRole('button', { name: 'Copy discovery notes' }))

    expect(writeText).toHaveBeenCalledWith('raw discovery text')
  })
})
