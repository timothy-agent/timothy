import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { PlanUnit } from '../../api/types'
import { PlanApprovalBanner } from './PlanApprovalBanner'

afterEach(cleanup)

const units: PlanUnit[] = [{ title: 'Add validation', verify_cmd: 'go test ./...', passes: false }]

describe('PlanApprovalBanner', () => {
  it('renders the plan units and assumptions', () => {
    render(
      <PlanApprovalBanner
        units={units}
        assumptions={[{ assumption: 'no language version was specified', default: 'Python 3.12' }]}
        onApprove={vi.fn()}
        onReplan={vi.fn()}
        onRediscover={vi.fn()}
      />,
    )
    expect(screen.getByText('Add validation')).toBeInTheDocument()
    expect(screen.getByText(/no language version was specified/)).toBeInTheDocument()
  })

  it('calls onApprove when Approve is clicked', () => {
    const onApprove = vi.fn()
    render(
      <PlanApprovalBanner units={units} onApprove={onApprove} onReplan={vi.fn()} onRediscover={vi.fn()} />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    expect(onApprove).toHaveBeenCalled()
  })

  it('calls onRediscover when Rediscover is clicked', () => {
    const onRediscover = vi.fn()
    render(
      <PlanApprovalBanner units={units} onApprove={vi.fn()} onReplan={vi.fn()} onRediscover={onRediscover} />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Rediscover' }))
    expect(onRediscover).toHaveBeenCalled()
  })

  it('reveals a textarea when Request replan is clicked, and submits its text', () => {
    const onReplan = vi.fn()
    render(
      <PlanApprovalBanner units={units} onApprove={vi.fn()} onReplan={onReplan} onRediscover={vi.fn()} />,
    )
    expect(screen.queryByPlaceholderText(/Optional feedback/)).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Request replan' }))
    const textarea = screen.getByPlaceholderText(/Optional feedback/)
    fireEvent.change(textarea, { target: { value: 'try a different **approach**' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))

    expect(onReplan).toHaveBeenCalledWith('try a different **approach**')
  })

  it('submits empty feedback when Send is clicked with no text typed', () => {
    const onReplan = vi.fn()
    render(
      <PlanApprovalBanner units={units} onApprove={vi.fn()} onReplan={onReplan} onRediscover={vi.fn()} />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Request replan' }))
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(onReplan).toHaveBeenCalledWith('')
  })

  it('shows a status line and hides the buttons once answered', () => {
    render(
      <PlanApprovalBanner
        units={units}
        answeredDecision="approve"
        onApprove={vi.fn()}
        onReplan={vi.fn()}
        onRediscover={vi.fn()}
      />,
    )
    expect(screen.getByText(/Approved/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
  })
})
