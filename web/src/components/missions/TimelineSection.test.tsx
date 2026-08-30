import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { MissionEvent } from '../../api/types'
import { TimelineSection } from './TimelineSection'

afterEach(cleanup)
beforeEach(() => {
  Object.assign(navigator, { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } })
})

const events: MissionEvent[] = [
  {
    mission_id: 'm1',
    seq: 1,
    kind: 'mission.done',
    payload: {},
    provenance: 'harness',
    created_at: '2026-01-01T00:00:00Z',
  },
]

describe('TimelineSection', () => {
  it('renders inline by default', () => {
    render(<TimelineSection events={events} />)
    expect(screen.getByRole('button', { name: 'Fullscreen' })).toBeTruthy()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('opens a fullscreen dialog with the same events on toggle', () => {
    render(<TimelineSection events={events} />)
    fireEvent.click(screen.getByRole('button', { name: 'Fullscreen' }))

    const dialog = screen.getByRole('dialog')
    expect(dialog).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Exit fullscreen' })).toBeTruthy()
    expect(screen.getAllByText('1 event')).toHaveLength(1)
  })

  it('closes the dialog on exit fullscreen', () => {
    render(<TimelineSection events={events} />)
    fireEvent.click(screen.getByRole('button', { name: 'Fullscreen' }))
    fireEvent.click(screen.getByRole('button', { name: 'Exit fullscreen' }))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('has a copy button that copies a plain-text rendering of the events', () => {
    const writeText = navigator.clipboard.writeText as ReturnType<typeof vi.fn>
    render(<TimelineSection events={events} />)

    fireEvent.click(screen.getByRole('button', { name: 'Copy timeline' }))

    expect(writeText).toHaveBeenCalledWith(expect.stringContaining('mission.done'))
  })

  it('shows a tooltip naming the action on hover', async () => {
    render(<TimelineSection events={events} />)
    const button = screen.getByRole('button', { name: 'Fullscreen' })

    fireEvent.pointerEnter(button)
    fireEvent.pointerMove(button)

    await waitFor(() => expect(screen.getAllByText('Fullscreen').length).toBeGreaterThan(0))
  })

  it('shows a "Copy" tooltip on the copy button', async () => {
    render(<TimelineSection events={events} />)
    const button = screen.getByRole('button', { name: 'Copy timeline' })

    fireEvent.pointerEnter(button.parentElement!)
    fireEvent.pointerMove(button.parentElement!)

    await waitFor(() => expect(screen.getByText('Copy')).toBeInTheDocument())
  })

  it('excludes executor.progress events from the rendered rows and count', () => {
    const withProgress: MissionEvent[] = [
      ...events,
      {
        mission_id: 'm1',
        seq: 2,
        kind: 'executor.progress',
        payload: { run_id: 'r1', byte_offset: 10, turns: 1, tool_calls: 2 },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:01Z',
      },
    ]
    render(<TimelineSection events={withProgress} />)
    expect(screen.getAllByText('1 event')).toHaveLength(1)
  })
})

// toolCallTraceEvents builds a mission.turn row preceded by three
// mission.tool_call events, ordered as runner.go's runTurn appends
// them: the shape a per-turn trace toggle groups (issue #369).
const toolCallTraceEvents: MissionEvent[] = [
  {
    mission_id: 'm1',
    seq: 1,
    kind: 'mission.tool_call',
    payload: { phase: 'execute', tool: 'search_kb', args_digest: '{"query":"first"}', status: 'ok', duration_ms: 12 },
    provenance: 'harness',
    created_at: '2026-01-01T00:00:00Z',
  },
  {
    mission_id: 'm1',
    seq: 2,
    kind: 'mission.tool_call',
    payload: { phase: 'execute', tool: 'shell', args_digest: '{"command":"ls"}', status: 'denied', duration_ms: 3 },
    provenance: 'harness',
    created_at: '2026-01-01T00:00:01Z',
  },
  {
    mission_id: 'm1',
    seq: 3,
    kind: 'mission.tool_call',
    payload: { phase: 'execute', tool: 'write_file', args_digest: '{"path":"x"}', status: 'error', duration_ms: 40 },
    provenance: 'harness',
    created_at: '2026-01-01T00:00:02Z',
  },
  {
    mission_id: 'm1',
    seq: 4,
    kind: 'mission.turn',
    payload: { phase: 'execute', duration_ms: 500, ok: true, input: 'worker_done' },
    provenance: 'harness',
    created_at: '2026-01-01T00:00:03Z',
  },
]

describe('TimelineSection tool call trace', () => {
  it('excludes mission.tool_call events from the plain row count', () => {
    render(<TimelineSection events={toolCallTraceEvents} />)
    // Only the mission.turn row counts as an event; its three tool
    // calls are nested, not separate rows.
    expect(screen.getAllByText('1 event')).toHaveLength(1)
  })

  it('shows the trace collapsed by default, with a toggle naming the count', () => {
    render(<TimelineSection events={toolCallTraceEvents} />)
    expect(screen.getByText('3 tool calls')).toBeTruthy()
    expect(screen.queryByText('search_kb')).toBeNull()
  })

  it('expands to show the ordered tool-call trace with outcome and duration', () => {
    render(<TimelineSection events={toolCallTraceEvents} />)
    fireEvent.click(screen.getByText('3 tool calls'))

    const names = screen.getAllByText(/search_kb|shell|write_file/).map((el) => el.textContent)
    expect(names).toEqual(['search_kb', 'shell', 'write_file'])
    expect(screen.getByText('{"query":"first"}')).toBeTruthy()
    expect(screen.getByText('{"command":"ls"}')).toBeTruthy()
    expect(screen.getByText('{"path":"x"}')).toBeTruthy()
  })

  it('collapses again on a second toggle click', () => {
    render(<TimelineSection events={toolCallTraceEvents} />)
    const toggle = screen.getByText('3 tool calls')
    fireEvent.click(toggle)
    expect(screen.getByText('search_kb')).toBeTruthy()
    fireEvent.click(toggle)
    expect(screen.queryByText('search_kb')).toBeNull()
  })

  it('shows no toggle for a turn with no tool calls', () => {
    const noCalls: MissionEvent[] = [
      {
        mission_id: 'm1',
        seq: 1,
        kind: 'mission.turn',
        payload: { phase: 'plan', duration_ms: 200, ok: true, input: 'plan_created' },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:00Z',
      },
    ]
    render(<TimelineSection events={noCalls} />)
    expect(screen.queryByText(/tool call/)).toBeNull()
  })
})
