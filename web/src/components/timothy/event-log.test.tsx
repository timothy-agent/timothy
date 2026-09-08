import type { ReactNode } from 'react'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { EventLog, type EventLogRow } from './event-log'

afterEach(cleanup)

function row(overrides: Partial<EventLogRow> & { id: string; time: Date }): EventLogRow {
  return { kind: 'note', title: 'An event', ...overrides }
}

function renderLog(children: ReactNode) {
  return render(<TooltipProvider>{children}</TooltipProvider>)
}

describe('EventLog', () => {
  it('renders rows with their times', () => {
    renderLog(
      <EventLog
        rows={[
          row({ id: '1', time: new Date('2026-09-07T08:00:00Z'), title: 'Mission started' }),
          row({ id: '2', time: new Date('2026-09-07T08:01:00Z'), title: 'Plan approved' }),
        ]}
      />,
    )
    expect(screen.getByText('Mission started')).toBeInTheDocument()
    expect(screen.getByText('Plan approved')).toBeInTheDocument()
  })

  it('renders a day divider when the calendar day changes', () => {
    renderLog(
      <EventLog
        rows={[
          row({ id: '1', time: new Date('2026-09-06T23:59:00Z'), title: 'Yesterday event' }),
          row({ id: '2', time: new Date('2026-09-07T00:01:00Z'), title: 'Today event' }),
        ]}
      />,
    )
    expect(screen.getAllByRole('presentation').length).toBeGreaterThanOrEqual(1)
  })

  it('toggles a disclosure and shows the payload', () => {
    renderLog(
      <EventLog
        rows={[row({ id: '1', time: new Date(), title: 'Tool call', payload: { tool: 'search_web' } })]}
      />,
    )
    const trigger = screen.getByRole('button', { name: /Tool call/ })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(trigger)
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText(/search_web/)).toBeInTheDocument()
  })

  it('reserves the chevron slot on rows without a disclosure so timestamps align', () => {
    renderLog(
      <EventLog
        rows={[
          row({ id: '1', time: new Date(), title: 'Plain' }),
          row({ id: '2', time: new Date(), title: 'With payload', payload: { a: 1 } }),
        ]}
      />,
    )
    const plain = screen.getByText('Plain').closest('li') as HTMLElement
    const spacer = plain.querySelector('span[aria-hidden]') as HTMLElement
    expect(spacer).toHaveClass('size-3.5', 'shrink-0')
    expect(spacer.nextElementSibling?.tagName).toBe('TIME')
    const withPayload = screen.getByText('With payload').closest('li') as HTMLElement
    expect(withPayload.querySelector('svg[data-chevron]')?.nextElementSibling?.tagName).toBe('TIME')
  })

  it('shows empty text when there are no rows', () => {
    renderLog(<EventLog rows={[]} emptyText="Nothing happened yet." />)
    expect(screen.getByText('Nothing happened yet.')).toBeInTheDocument()
  })

  it('is a log region', () => {
    renderLog(<EventLog rows={[]} />)
    expect(screen.getByRole('log')).toBeInTheDocument()
  })

  it('toggles follow state and shows a "new" badge while paused', () => {
    const { rerender } = renderLog(
      <EventLog rows={[row({ id: '1', time: new Date(), title: 'One' })]} />,
    )
    const toggle = screen.getByRole('button', { name: /Pause/ })
    expect(toggle).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(toggle)
    expect(screen.getByRole('button', { name: /Follow/ })).toHaveAttribute('aria-pressed', 'false')

    rerender(
      <TooltipProvider>
        <EventLog
          rows={[
            row({ id: '1', time: new Date(), title: 'One' }),
            row({ id: '2', time: new Date(), title: 'Two' }),
          ]}
        />
      </TooltipProvider>,
    )
    expect(screen.getByText('1 new')).toBeInTheDocument()
  })

  it('clicking the "N new" badge jumps to the bottom and resumes following', () => {
    Element.prototype.scrollTo = vi.fn()
    const { rerender } = renderLog(
      <EventLog rows={[row({ id: '1', time: new Date(), title: 'One' })]} />,
    )
    fireEvent.click(screen.getByRole('button', { name: /Pause/ }))
    rerender(
      <TooltipProvider>
        <EventLog
          rows={[
            row({ id: '1', time: new Date(), title: 'One' }),
            row({ id: '2', time: new Date(), title: 'Two' }),
          ]}
        />
      </TooltipProvider>,
    )
    const badge = screen.getByRole('button', { name: '1 new' })
    fireEvent.click(badge)
    expect(screen.getByRole('button', { name: /Pause/ })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.queryByText('1 new')).not.toBeInTheDocument()
  })

  it('fills the remaining height instead of capping at 32rem when fill is set', () => {
    renderLog(<EventLog rows={[row({ id: '1', time: new Date(), title: 'A' })]} fill />)
    const log = screen.getByRole('log')
    expect(log).toHaveClass('min-h-0', 'flex-1')
    expect(log).not.toHaveClass('max-h-[32rem]')
  })

  it('grows with its rows up to 32rem by default', () => {
    renderLog(<EventLog rows={[row({ id: '1', time: new Date(), title: 'A' })]} />)
    const log = screen.getByRole('log')
    expect(log).toHaveClass('max-h-[32rem]')
    expect(log).not.toHaveClass('flex-1')
  })

  it('pauses following when the user scrolls away from the bottom', () => {
    renderLog(<EventLog rows={[row({ id: '1', time: new Date(), title: 'One' })]} />)
    const log = screen.getByRole('log')
    Object.defineProperty(log, 'scrollHeight', { value: 1000, configurable: true })
    Object.defineProperty(log, 'scrollTop', { value: 0, configurable: true })
    Object.defineProperty(log, 'clientHeight', { value: 200, configurable: true })
    fireEvent.scroll(log)
    expect(screen.getByRole('button', { name: /Follow/ })).toHaveAttribute('aria-pressed', 'false')
  })

  it('calls onFollowChange instead of managing state internally when controlled', () => {
    const onFollowChange = vi.fn()
    renderLog(
      <EventLog
        rows={[row({ id: '1', time: new Date(), title: 'One' })]}
        follow={true}
        onFollowChange={onFollowChange}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: /Pause/ }))
    expect(onFollowChange).toHaveBeenCalledWith(false)
  })

  it('gives labelled rows their own column, reserved even on rows without one', () => {
    renderLog(
      <EventLog
        rows={[
          row({ id: '1', time: new Date(), title: 'First', label: 'plan' }),
          row({ id: '2', time: new Date(), title: 'Second' }),
        ]}
      />,
    )
    const labelled = screen.getByText('First').closest('li') as HTMLElement
    expect(labelled.querySelector('.w-20')?.textContent).toBe('plan')
    const bare = screen.getByText('Second').closest('li') as HTMLElement
    expect(bare.querySelector('.w-20')).not.toBeNull()
    expect(bare.querySelector('.w-20')?.textContent).toBe('')
  })

  it('drops the label column when no row carries a label', () => {
    renderLog(<EventLog rows={[row({ id: '1', time: new Date(), title: 'Only' })]} />)
    const item = screen.getByText('Only').closest('li') as HTMLElement
    expect(item.querySelector('.w-20')).toBeNull()
  })
})
