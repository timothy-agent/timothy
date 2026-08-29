import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { applyEvent, emptyAssistant, type AssistantState } from '../lib/chat'
import type { ChatEvent } from '../api/types'
import {
  ActivityLine,
  ActivityPanel,
  prettyArgs,
  summarizeTools,
  totalDuration,
} from './Activity'
import { formatDuration } from '../lib/format'
import { Sheet } from './ui/sheet'

afterEach(cleanup)

function play(events: ChatEvent[]): AssistantState {
  return events.reduce(applyEvent, emptyAssistant())
}

describe('formatDuration', () => {
  it('renders sub-second durations in milliseconds', () => {
    expect(formatDuration(42)).toBe('42ms')
  })

  it('renders durations of a second or more in seconds', () => {
    expect(formatDuration(1500)).toBe('1.5s')
  })
})

describe('prettyArgs', () => {
  it('pretty-prints valid JSON', () => {
    expect(prettyArgs('{"a":1}')).toBe('{\n  "a": 1\n}')
  })

  it('falls back to the raw string for invalid JSON', () => {
    expect(prettyArgs('not json')).toBe('not json')
  })
})

describe('totalDuration', () => {
  it('sums the duration of every tool that reported one', () => {
    expect(
      totalDuration([
        { id: 'c1', name: 'a', status: 'ok', durationMs: 10 },
        { id: 'c2', name: 'b', status: 'ok', durationMs: 20 },
      ]),
    ).toBe(30)
  })

  it('treats a still-running call (no durationMs) as contributing nothing', () => {
    expect(
      totalDuration([
        { id: 'c1', name: 'a', status: 'ok', durationMs: 10 },
        { id: 'c2', name: 'b', status: 'running' },
      ]),
    ).toBe(10)
  })
})

describe('summarizeTools', () => {
  it('dedupes by first appearance and counts repeats', () => {
    expect(
      summarizeTools([
        { id: 'c1', name: 'search_web', status: 'ok' },
        { id: 'c2', name: 'search_web', status: 'ok' },
        { id: 'c3', name: 'fetch_url', status: 'ok' },
      ]),
    ).toBe('2× search_web, fetch_url')
  })

  it('caps at three names and folds the rest into a "+N more"', () => {
    expect(
      summarizeTools([
        { id: 'c1', name: 'a', status: 'ok' },
        { id: 'c2', name: 'b', status: 'ok' },
        { id: 'c3', name: 'c', status: 'ok' },
        { id: 'c4', name: 'd', status: 'ok' },
        { id: 'c5', name: 'e', status: 'ok' },
      ]),
    ).toBe('a, b, c, +2 more')
  })
})

describe('ActivityLine', () => {
  it('renders nothing for a turn with no tools and no reasoning', () => {
    const msg = play([{ type: 'chunk', text: 'hi' }, { type: 'meta', session_id: 's' }])
    render(<ActivityLine msg={msg} onOpen={vi.fn()} />)
    expect(screen.queryByTestId('activity-line')).not.toBeInTheDocument()
  })

  it('shows "Running <name>…" while a tool is in flight mid-stream', () => {
    const msg = play([{ type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } }])
    render(<ActivityLine msg={msg} onOpen={vi.fn()} />)
    expect(screen.getByTestId('activity-line')).toHaveTextContent('Running shell…')
  })

  it('shows "Thinking…" while streaming with only reasoning so far', () => {
    const msg = play([{ type: 'reasoning_chunk', text: 'hmm' }])
    render(<ActivityLine msg={msg} onOpen={vi.fn()} />)
    expect(screen.getByTestId('activity-line')).toHaveTextContent('Thinking…')
  })

  it('shows "Worked for <duration> · <summary>" once done', () => {
    const msg = play([
      { type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } },
      { type: 'tool_result', tool_result: { id: 'c1', name: 'shell', status: 'ok', duration_ms: 42 } },
      { type: 'meta', session_id: 's' },
    ])
    render(<ActivityLine msg={msg} onOpen={vi.fn()} />)
    expect(screen.getByTestId('activity-line')).toHaveTextContent('Worked for 42ms · shell')
  })

  it('falls back to "Ran <n> tools" when the total duration is zero', () => {
    const msg = play([
      { type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } },
      { type: 'tool_result', tool_result: { id: 'c1', name: 'shell', status: 'ok', duration_ms: 0 } },
      { type: 'meta', session_id: 's' },
    ])
    render(<ActivityLine msg={msg} onOpen={vi.fn()} />)
    expect(screen.getByTestId('activity-line')).toHaveTextContent('Ran 1 tools · shell')
  })

  it('shows "Reasoning" once done with reasoning but no tools', () => {
    const msg = play([
      { type: 'reasoning_chunk', text: 'hmm' },
      { type: 'chunk', text: 'answer' },
      { type: 'meta', session_id: 's' },
    ])
    render(<ActivityLine msg={msg} onOpen={vi.fn()} />)
    expect(screen.getByTestId('activity-line')).toHaveTextContent('Reasoning')
  })

  it('shows a red dot when any tool errored', () => {
    const msg = play([
      { type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } },
      { type: 'tool_result', tool_result: { id: 'c1', name: 'shell', status: 'error', duration_ms: 5 } },
      { type: 'meta', session_id: 's' },
    ])
    const { container } = render(<ActivityLine msg={msg} onOpen={vi.fn()} />)
    expect(container.querySelector('.bg-red-500')).not.toBeNull()
  })

  it('shows an amber dot when any tool was denied', () => {
    const msg = play([
      { type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } },
      { type: 'tool_result', tool_result: { id: 'c1', name: 'shell', status: 'denied', duration_ms: 5 } },
      { type: 'meta', session_id: 's' },
    ])
    const { container } = render(<ActivityLine msg={msg} onOpen={vi.fn()} />)
    expect(container.querySelector('.bg-amber-500')).not.toBeNull()
  })

  it('calls onOpen when clicked', () => {
    const msg = play([
      { type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } },
      { type: 'tool_result', tool_result: { id: 'c1', name: 'shell', status: 'ok', duration_ms: 5 } },
      { type: 'meta', session_id: 's' },
    ])
    const onOpen = vi.fn()
    render(<ActivityLine msg={msg} onOpen={onOpen} />)
    fireEvent.click(screen.getByTestId('activity-line'))
    expect(onOpen).toHaveBeenCalledOnce()
  })
})

describe('ActivityPanel', () => {
  it('renders reasoning and every tool call with status, duration, args, and digest', () => {
    const msg = play([
      { type: 'reasoning_chunk', text: 'thinking it through' },
      { type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } },
      { type: 'tool_end', tool_call: { id: 'c1', name: 'shell', input: { command: 'ls' } } },
      {
        type: 'tool_result',
        tool_result: { id: 'c1', name: 'shell', status: 'ok', digest: 'notes.md', duration_ms: 42 },
      },
      { type: 'meta', session_id: 's' },
    ])
    render(
      <Sheet open>
        <ActivityPanel msg={msg} />
      </Sheet>,
    )
    expect(screen.getByText('Activity')).toBeInTheDocument()
    expect(screen.getByText('thinking it through')).toBeInTheDocument()
    expect(screen.getByTestId('tool-status')).toHaveTextContent('ok')
    expect(screen.getByText('notes.md')).toBeInTheDocument()
    expect(screen.getByText('42ms')).toBeInTheDocument()
  })

  it('pretty-prints args as JSON', () => {
    const msg = play([
      { type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } },
      { type: 'tool_end', tool_call: { id: 'c1', name: 'shell', input: { command: 'ls' } } },
      { type: 'tool_result', tool_result: { id: 'c1', name: 'shell', status: 'ok', duration_ms: 1 } },
    ])
    render(
      <Sheet open>
        <ActivityPanel msg={msg} />
      </Sheet>,
    )
    expect(screen.getByText(/"command": "ls"/)).toBeInTheDocument()
  })

  it('shows a running badge for a tool honestly left running (e.g. an aborted turn)', () => {
    const msg = play([{ type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } }])
    render(
      <Sheet open>
        <ActivityPanel msg={msg} />
      </Sheet>,
    )
    expect(screen.getByTestId('tool-status')).toHaveTextContent('running…')
  })

  it('copies the reasoning text', () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })

    const msg = play([{ type: 'reasoning_chunk', text: 'the reasoning' }])
    render(
      <Sheet open>
        <ActivityPanel msg={msg} />
      </Sheet>,
    )
    fireEvent.click(screen.getByLabelText('Copy reasoning'))
    expect(writeText).toHaveBeenCalledWith('the reasoning')
    vi.unstubAllGlobals()
  })

  it('copies a tool result digest', () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })

    const msg = play([
      { type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } },
      {
        type: 'tool_result',
        tool_result: { id: 'c1', name: 'shell', status: 'ok', digest: 'the digest', duration_ms: 1 },
      },
    ])
    render(
      <Sheet open>
        <ActivityPanel msg={msg} />
      </Sheet>,
    )
    fireEvent.click(screen.getByLabelText('Copy tool response'))
    expect(writeText).toHaveBeenCalledWith('the digest')
    vi.unstubAllGlobals()
  })

  it('shows the tool count and total duration in the header', () => {
    const msg = play([
      { type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } },
      { type: 'tool_result', tool_result: { id: 'c1', name: 'shell', status: 'ok', duration_ms: 42 } },
      { type: 'tool_start', tool_call: { id: 'c2', name: 'search_web' } },
      {
        type: 'tool_result',
        tool_result: { id: 'c2', name: 'search_web', status: 'ok', duration_ms: 58 },
      },
      { type: 'meta', session_id: 's' },
    ])
    render(
      <Sheet open>
        <ActivityPanel msg={msg} />
      </Sheet>,
    )
    expect(screen.getByText('2 tools · 100ms')).toBeInTheDocument()
  })
})
