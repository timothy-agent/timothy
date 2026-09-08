import { useState, type ReactNode } from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ApprovalCard, ApprovalDialog } from './ApprovalCard'
import { consequenceLine } from '@/lib/chatUi'
import type { PermissionRequestEvent } from '@/api/types'

function renderWithProvider(children: ReactNode) {
  return render(<TooltipProvider>{children}</TooltipProvider>)
}

const baseRequest: PermissionRequestEvent = {
  id: 'perm-1',
  call_id: 'call-1',
  tool: 'shell',
  args: JSON.stringify({ command: 'go test ./...' }),
  danger_level: 'safe',
  rationale: 'Confirms the change did not regress tests.',
}

describe('consequenceLine', () => {
  it('is safe and reversible by default', () => {
    expect(consequenceLine({ ...baseRequest, tool: 'read_file', danger_level: 'safe' })).toContain('reversible')
  })

  it('warns about destructive tools', () => {
    expect(consequenceLine({ ...baseRequest, danger_level: 'destructive' })).toContain('may not be reversible')
  })

  it('adds a shell command note', () => {
    expect(consequenceLine(baseRequest)).toContain('Executes a shell command.')
  })

  it('adds an external traffic note for web tools', () => {
    expect(consequenceLine({ ...baseRequest, tool: 'search_web', danger_level: 'safe' })).toContain('Sends traffic outside Timothy.')
  })
})

describe('ApprovalCard', () => {
  it('renders the tool in mono, rationale, consequence, and three actions', () => {
    renderWithProvider(<ApprovalCard request={baseRequest} onDecision={() => {}} />)
    expect(screen.getByText('shell').tagName).toBe('CODE')
    expect(screen.getByText(baseRequest.rationale)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Deny/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Allow for session/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Allow once/ })).toBeInTheDocument()
  })

  it('shows a danger badge for destructive requests', () => {
    renderWithProvider(<ApprovalCard request={{ ...baseRequest, danger_level: 'destructive' }} onDecision={() => {}} />)
    expect(screen.getByTestId('danger-badge')).toBeInTheDocument()
  })

  it('has no danger badge for safe requests', () => {
    renderWithProvider(<ApprovalCard request={baseRequest} onDecision={() => {}} />)
    expect(screen.queryByTestId('danger-badge')).not.toBeInTheDocument()
  })

  it('calls onDecision(id, once) when a is pressed with focus inside the card', () => {
    const onDecision = vi.fn()
    renderWithProvider(<ApprovalCard request={baseRequest} onDecision={onDecision} />)
    fireEvent.keyDown(screen.getByRole('region'), { key: 'a' })
    expect(onDecision).toHaveBeenCalledWith('perm-1', 'once')
  })

  it('does nothing when d is pressed with focus outside the card', () => {
    const onDecision = vi.fn()
    renderWithProvider(
      <div>
        <input data-testid="outside" />
        <ApprovalCard request={baseRequest} onDecision={onDecision} />
      </div>,
    )
    fireEvent.keyDown(screen.getByTestId('outside'), { key: 'd' })
    expect(onDecision).not.toHaveBeenCalled()
  })

  it('calls onDecision(id, session) when s is pressed', () => {
    const onDecision = vi.fn()
    renderWithProvider(<ApprovalCard request={baseRequest} onDecision={onDecision} />)
    fireEvent.keyDown(screen.getByRole('region'), { key: 's' })
    expect(onDecision).toHaveBeenCalledWith('perm-1', 'session')
  })

  it('calls onDecision(id, deny) when the Deny button is clicked', () => {
    const onDecision = vi.fn()
    renderWithProvider(<ApprovalCard request={baseRequest} onDecision={onDecision} />)
    fireEvent.click(screen.getByRole('button', { name: /Deny/ }))
    expect(onDecision).toHaveBeenCalledWith('perm-1', 'deny')
  })

  it('calls onDecision(id, session) when the Allow for session button is clicked', () => {
    const onDecision = vi.fn()
    renderWithProvider(<ApprovalCard request={baseRequest} onDecision={onDecision} />)
    fireEvent.click(screen.getByRole('button', { name: /Allow for session/ }))
    expect(onDecision).toHaveBeenCalledWith('perm-1', 'session')
  })

  it('renders raw args text when it is not valid JSON', () => {
    renderWithProvider(
      <ApprovalCard request={{ ...baseRequest, args: 'not json' }} onDecision={() => {}} />,
    )
    expect(screen.getByText('not json', { exact: false })).toBeInTheDocument()
  })

  it('titles a non-shell tool as "wants to use"', () => {
    renderWithProvider(
      <ApprovalCard request={{ ...baseRequest, tool: 'read_file' }} onDecision={() => {}} />,
    )
    expect(screen.getByText(/Timothy wants to use/)).toBeInTheDocument()
  })

  it('calls onDecision(id, deny) when d is pressed', () => {
    const onDecision = vi.fn()
    renderWithProvider(<ApprovalCard request={baseRequest} onDecision={onDecision} />)
    fireEvent.keyDown(screen.getByRole('region'), { key: 'd' })
    expect(onDecision).toHaveBeenCalledWith('perm-1', 'deny')
  })

  it('calls onDecision(id, once) when the Allow once button is clicked', () => {
    const onDecision = vi.fn()
    renderWithProvider(<ApprovalCard request={baseRequest} onDecision={onDecision} />)
    fireEvent.click(screen.getByRole('button', { name: /Allow once/ }))
    expect(onDecision).toHaveBeenCalledWith('perm-1', 'once')
  })

  it('shows the formatted requested time in meta when provided', () => {
    renderWithProvider(
      <ApprovalCard
        request={baseRequest}
        onDecision={() => {}}
        requestedAt={new Date('2026-09-07T14:30:00Z')}
      />,
    )
    expect(screen.getByText(/\d{2}:\d{2}/)).toBeInTheDocument()
  })

  it('omits the rationale blockquote when rationale is empty', () => {
    const { container } = renderWithProvider(
      <ApprovalCard request={{ ...baseRequest, rationale: '' }} onDecision={() => {}} />,
    )
    expect(container.querySelector('blockquote')).not.toBeInTheDocument()
  })
})

function DialogHarness({ onDecision }: { onDecision: (id: string, d: 'once' | 'session' | 'deny') => void }) {
  const [open, setOpen] = useState(true)
  return <ApprovalDialog request={baseRequest} onDecision={onDecision} open={open} onOpenChange={setOpen} />
}

describe('ApprovalDialog', () => {
  it('calls onOpenChange(false) on Escape without deciding', () => {
    const onDecision = vi.fn()
    renderWithProvider(<DialogHarness onDecision={onDecision} />)
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(onDecision).not.toHaveBeenCalled()
  })
})
