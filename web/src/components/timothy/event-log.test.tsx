import type { ReactNode } from 'react'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
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

  it('renders a status dot when status is set', () => {
    const { container } = renderLog(
      <EventLog rows={[row({ id: '1', time: new Date(), title: 'Done', status: 'success' })]} />,
    )
    expect(container.querySelector('[data-status="success"]')).toBeInTheDocument()
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
})
