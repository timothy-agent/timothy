import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ResultSection } from './ResultSection'

describe('ResultSection', () => {
  it('renders the evidence text as markdown', () => {
    render(<ResultSection evidence={'**bold** and a [link](https://example.com)'} />)
    expect(screen.getByRole('strong')).toHaveTextContent('bold')
    expect(screen.getByRole('link')).toHaveAttribute('href', 'https://example.com')
  })
})
