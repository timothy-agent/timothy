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

  it('shows a denied status line for a deny decision', () => {
    renderGate({ tool: 'shell', onDecide: vi.fn(), answeredDecision: 'deny' })
    expect(screen.getByText('Denied — returning to worker…')).toBeInTheDocument()
  })

  it('shows an unknown status line for an unknown decision', () => {
    renderGate({ tool: 'shell', onDecide: vi.fn(), answeredDecision: 'unknown' })
    expect(
      screen.getByText('Answered — waiting for the worker to continue…'),
    ).toBeInTheDocument()
  })

  it('renders no arguments block when args is undefined', () => {
    renderGate({ tool: 'shell', onDecide: vi.fn() })
    expect(screen.queryByText('Arguments')).not.toBeInTheDocument()
  })

  it('renders no arguments block when args is an empty string', () => {
    renderGate({ tool: 'shell', args: '', onDecide: vi.fn() })
    expect(screen.queryByText('Arguments')).not.toBeInTheDocument()
  })

  it('renders non-JSON args raw rather than throwing', () => {
    renderGate({ tool: 'shell', args: 'not json', onDecide: vi.fn() })
    expect(screen.getByText('not json', { exact: false })).toBeInTheDocument()
  })

  it('calls onDecide for the deny button', () => {
    const onDecide = vi.fn()
    renderGate({ tool: 'shell', onDecide })
    fireEvent.click(screen.getByRole('button', { name: 'Deny' }))
    expect(onDecide).toHaveBeenCalledWith('deny')
  })

  it('calls onDecide for the allow-for-session button', () => {
    const onDecide = vi.fn()
    renderGate({ tool: 'shell', onDecide })
    fireEvent.click(screen.getByRole('button', { name: 'Allow for session' }))
    expect(onDecide).toHaveBeenCalledWith('session')
  })

  it('uses a generic title when no tool is given', () => {
    renderGate({ onDecide: vi.fn() })
    expect(screen.getByText('Timothy wants to use this tool')).toBeInTheDocument()
  })

  it('does not show a destructive badge for a non-destructive request', () => {
    renderGate({ tool: 'shell', danger: 'safe', onDecide: vi.fn() })
    expect(screen.queryByText('destructive')).not.toBeInTheDocument()
  })

  it('answers the a/s/d keyboard shortcuts', () => {
    const onDecide = vi.fn()
    renderGate({ tool: 'shell', onDecide })
    fireEvent.keyDown(screen.getByRole('region'), { key: 'a' })
    expect(onDecide).toHaveBeenCalledWith('once')
    fireEvent.keyDown(screen.getByRole('region'), { key: 's' })
    expect(onDecide).toHaveBeenCalledWith('session')
    fireEvent.keyDown(screen.getByRole('region'), { key: 'd' })
    expect(onDecide).toHaveBeenCalledWith('deny')
  })

  it('ignores keyboard shortcuts once answered', () => {
    const onDecide = vi.fn()
    renderGate({ tool: 'shell', onDecide, answeredDecision: 'once' })
    fireEvent.keyDown(screen.getByRole('region'), { key: 'a' })
    expect(onDecide).not.toHaveBeenCalled()
  })
})
