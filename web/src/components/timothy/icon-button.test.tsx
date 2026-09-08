import { Trash2 } from 'lucide-react'
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { IconButton } from './icon-button'

afterEach(cleanup)

describe('IconButton', () => {
  it('has an accessible label', () => {
    render(
      <TooltipProvider>
        <IconButton label="Delete" icon={Trash2} tooltip={false} />
      </TooltipProvider>,
    )
    expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument()
  })

  it('sets aria-busy and disabled while loading', () => {
    render(
      <TooltipProvider>
        <IconButton label="Save" icon={Trash2} tooltip={false} loading />
      </TooltipProvider>,
    )
    const button = screen.getByRole('button', { name: 'Save' })
    expect(button).toHaveAttribute('aria-busy', 'true')
    expect(button).toBeDisabled()
  })
})
