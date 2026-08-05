import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ProgressSection } from './ProgressSection'

describe('ProgressSection', () => {
  it('shows a placeholder when there are no notes', () => {
    render(<ProgressSection notes={[]} />)
    expect(screen.getByText('No progress notes yet.')).toBeInTheDocument()
  })

  it('renders each note in its own container with markdown applied', () => {
    render(
      <ProgressSection
        notes={[
          { at: '2026-01-01T00:00:00Z', note: '**bold** and a [link](https://example.com)' },
          { at: '2026-01-02T00:00:00Z', note: 'plain text' },
        ]}
      />,
    )
    const items = screen.getAllByRole('listitem')
    expect(items).toHaveLength(2)
    expect(items[0].querySelector('strong')).toHaveTextContent('bold')
    expect(items[0].querySelector('a')).toHaveAttribute('href', 'https://example.com')
    expect(items[1]).toHaveTextContent('plain text')
  })
})
