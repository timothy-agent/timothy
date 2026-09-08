import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { JsonBlock } from './json-block'

afterEach(cleanup)

describe('JsonBlock', () => {
  beforeEach(() => {
    Object.assign(navigator, {
      clipboard: { writeText: vi.fn().mockResolvedValue(undefined) },
    })
  })

  it('pretty-prints an object', () => {
    render(<JsonBlock value={{ a: 1 }} copy={false} />)
    expect(screen.getByText(/"a": 1/)).toBeInTheDocument()
  })

  it('truncates long output and expands on click', () => {
    const value = Array.from({ length: 30 }, (_, i) => `line-${i}`).join('\n')
    render(
      <TooltipProvider>
        <JsonBlock value={value} maxLines={5} />
      </TooltipProvider>,
    )
    expect(screen.getByText((_, node) => node?.textContent === value.split('\n').slice(0, 5).join('\n'))).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /Show all/ }))
    expect(screen.getByText((_, node) => node?.textContent === value)).toBeInTheDocument()
  })
})
