import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { applyEvent, emptyAssistant, type AssistantState } from '../lib/chat'
import type { ChatEvent } from '../api/types'
import { ActivityPanel } from './Activity'
import { prettyArgs, summarizeTools, totalDuration } from '../lib/activity'
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

describe('ActivityPanel', () => {
  it('renders reasoning and every tool call via ToolCallCard', () => {
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
    expect(screen.getByText('Shell')).toBeInTheDocument()
    expect(screen.getByText('42ms')).toBeInTheDocument()
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

  it('renders no reasoning block when the turn has none', () => {
    const msg = play([
      { type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } },
      { type: 'tool_result', tool_result: { id: 'c1', name: 'shell', status: 'ok', duration_ms: 1 } },
      { type: 'meta', session_id: 's' },
    ])
    render(
      <Sheet open>
        <ActivityPanel msg={msg} />
      </Sheet>,
    )
    expect(screen.queryByText('thinking it through')).not.toBeInTheDocument()
  })
})
