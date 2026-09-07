import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { CopyButton } from './copy-button'

afterEach(cleanup)

describe('CopyButton', () => {
  beforeEach(() => {
    Object.assign(navigator, {
      clipboard: { writeText: vi.fn().mockResolvedValue(undefined) },
    })
  })

  it('copies the value and announces Copied', async () => {
    render(
      <TooltipProvider>
        <CopyButton value="hello" />
      </TooltipProvider>,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('hello')
    expect(await screen.findByRole('status')).toHaveTextContent('Copied')
  })
})
