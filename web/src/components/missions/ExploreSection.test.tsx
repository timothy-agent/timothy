import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { ExploreSection } from './ExploreSection'

afterEach(cleanup)

describe('ExploreSection', () => {
  it('starts collapsed, showing only the trigger', () => {
    render(<ExploreSection notes="**bold** finding and a [link](https://example.com)" />)
    expect(screen.getByText('Show exploration')).toBeInTheDocument()
    expect(screen.queryByText('bold')).not.toBeInTheDocument()
  })

  it('reveals the notes as markdown once expanded', () => {
    render(<ExploreSection notes="**bold** finding and a [link](https://example.com)" />)
    fireEvent.click(screen.getByText('Show exploration'))

    expect(screen.getByText('bold').tagName).toBe('STRONG')
    expect(screen.getByRole('link')).toHaveAttribute('href', 'https://example.com')
  })
})
