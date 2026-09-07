import type { ComponentProps } from 'react'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '../ui/tooltip'
import { MissionPermissionGate } from './MissionPermissionGate'

afterEach(cleanup)

function renderGate(props: ComponentProps<typeof MissionPermissionGate>) {
  return render(
    <TooltipProvider>
      <MissionPermissionGate {...props} />
    </TooltipProvider>,
  )
}

describe('MissionPermissionGate', () => {
  it('is a labeled region naming the tool', () => {
    renderGate({
      tool: 'shell',
      args: '{"command":"rm -rf /tmp/x"}',
      danger: 'destructive',
      rationale: 'deletes files',
      onDecide: vi.fn(),
    })
    expect(screen.getByRole('region')).toBeInTheDocument()
    expect(screen.getByText('shell')).toBeInTheDocument()
    expect(screen.getByText('destructive')).toBeInTheDocument()
    expect(screen.getByText('deletes files')).toBeInTheDocument()
  })

  it('renders the arguments as formatted JSON', () => {
    renderGate({ tool: 'shell', args: '{"command":"rm -rf /tmp/x"}', onDecide: vi.fn() })
    expect(screen.getByText('"command": "rm -rf /tmp/x"', { exact: false })).toBeInTheDocument()
  })

  it('calls onDecide with the clicked decision', () => {
    const onDecide = vi.fn()
    renderGate({ tool: 'shell', onDecide })
    fireEvent.click(screen.getByRole('button', { name: 'Allow once' }))
    expect(onDecide).toHaveBeenCalledWith('once')
  })

  it('shows the auto-deny timeout when set', () => {
    renderGate({ tool: 'shell', onDecide: vi.fn(), timeoutSeconds: 30 })
    expect(screen.getByText('Auto-denies if unanswered for 30s')).toBeInTheDocument()
  })

  it('replaces the actions with a status line once answered', () => {
    renderGate({ tool: 'shell', onDecide: vi.fn(), answeredDecision: 'once' })
    expect(screen.getByText('Approved — command running…')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Allow once' })).not.toBeInTheDocument()
  })
})
