import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { PermissionRequestEvent } from '../api/types'
import { PermissionModal } from './PermissionModal'

afterEach(cleanup)

const request: PermissionRequestEvent = {
  id: 'p1',
  call_id: 'c1',
  tool: 'shell',
  args: '{"command":"rm -rf build/"}',
  danger_level: 'destructive',
  rationale: 'destructive command pattern: rm',
}

describe('PermissionModal', () => {
  it('shows tool, danger badge, rationale, and pretty args', () => {
    render(<PermissionModal request={request} onDecision={() => {}} />)
    const modal = screen.getByTestId('permission-modal')
    expect(modal).toHaveTextContent('shell')
    expect(screen.getByTestId('danger-badge')).toHaveTextContent('destructive')
    expect(modal).toHaveTextContent('destructive command pattern: rm')
    expect(modal).toHaveTextContent('rm -rf build/')
  })

  it('wraps a long unbroken argument value instead of overflowing the card', () => {
    const longValue = 'a'.repeat(200)
    render(
      <PermissionModal
        request={{ ...request, args: JSON.stringify({ url: longValue }) }}
        onDecision={() => {}}
      />,
    )
    const pre = screen.getByTestId('permission-modal').querySelector('pre')
    expect(pre).toHaveTextContent(longValue)
    expect(pre?.className).toContain('break-words')
    expect(pre?.className).toContain('min-w-0')
  })

  it('delivers each button decision', () => {
    for (const [label, decision] of [
      ['Allow once', 'once'],
      ['Allow for session', 'session'],
      ['Deny', 'deny'],
    ] as const) {
      const onDecision = vi.fn()
      render(<PermissionModal request={request} onDecision={onDecision} />)
      fireEvent.click(screen.getByRole('button', { name: new RegExp(label) }))
      expect(onDecision).toHaveBeenCalledWith('p1', decision)
      cleanup()
    }
  })

  it('answers keyboard shortcuts', () => {
    const onDecision = vi.fn()
    render(<PermissionModal request={request} onDecision={onDecision} />)
    fireEvent.keyDown(window, { key: 'a' })
    expect(onDecision).toHaveBeenCalledWith('p1', 'once')
  })

  it('treats dismissal as deny', () => {
    const onDecision = vi.fn()
    render(<PermissionModal request={request} onDecision={onDecision} />)
    fireEvent.click(screen.getByRole('button', { name: /close/i }))
    expect(onDecision).toHaveBeenCalledWith('p1', 'deny')
  })
})
