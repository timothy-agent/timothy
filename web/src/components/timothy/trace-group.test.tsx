import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { TraceGroup, TraceRow } from './trace-group'

afterEach(cleanup)

describe('TraceGroup', () => {
  it('keeps the action on one line and lets the target truncate', () => {
    render(<TraceRow status="success" action="Fetch url" target="https://example.com/a/very/long/path" />)
    expect(screen.getByText('Fetch url')).toHaveClass('shrink-0', 'whitespace-nowrap')
    expect(screen.getByText('https://example.com/a/very/long/path')).toHaveClass('min-w-0', 'truncate')
  })

  it('toggles aria-expanded and content visibility', () => {
    render(
      <TraceGroup summary="3 tool calls" duration="2.1s">
        <div>call detail</div>
      </TraceGroup>,
    )
    const trigger = screen.getByRole('button', { name: /3 tool calls/ })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(trigger)
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('call detail')).toBeInTheDocument()
  })

  it('appends the count to the summary when given', () => {
    render(<TraceGroup summary="Tool calls" count={5} />)
    expect(screen.getByText('Tool calls · 5')).toBeInTheDocument()
  })

  it('omits the count suffix when not given', () => {
    render(<TraceGroup summary="Tool calls" />)
    expect(screen.getByText('Tool calls')).toBeInTheDocument()
  })
})

describe('TraceRow', () => {
  it('renders as a button with aria-expanded when it has children', () => {
    render(
      <TraceRow status="success" action="Read" target="src/loop.go">
        <div>result</div>
      </TraceRow>,
    )
    const trigger = screen.getByRole('button')
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    fireEvent.click(trigger)
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('result')).toBeInTheDocument()
  })

  it('renders as a plain row without children', () => {
    render(<TraceRow status="working" action="Run" target="go test ./..." />)
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
    expect(screen.getByText('Run')).toBeInTheDocument()
  })
})
