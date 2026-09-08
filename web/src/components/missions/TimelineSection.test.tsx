import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { vi } from 'vitest'
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
  it('renders a themed log with no hard-coded dark surface', () => {
    render(<TimelineSection events={events} />)
    expect(screen.getByRole('button', { name: 'Fullscreen' })).toBeTruthy()
    expect(screen.queryByRole('dialog')).toBeNull()
    const log = screen.getByRole('log', { name: 'Mission timeline' })
    expect(log.className).not.toMatch(/zinc|amber-|green-|red-/)
  })

  it('shows an auto-follow Pause toggle', () => {
    render(<TimelineSection events={events} />)
    expect(screen.getByRole('button', { name: 'Pause' })).toBeTruthy()
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

  it('scrolls the log container via the scroll-to-top/bottom controls', () => {
    render(<TimelineSection events={events} />)
    expect(screen.getByRole('button', { name: 'Scroll to top' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Scroll to bottom' })).toBeTruthy()

    const scrollTo = vi.fn()
    Element.prototype.scrollTo = scrollTo
    fireEvent.click(screen.getByRole('button', { name: 'Scroll to top' }))
    expect(scrollTo).toHaveBeenCalledWith({ top: 0, behavior: 'smooth' })

    fireEvent.click(screen.getByRole('button', { name: 'Scroll to bottom' }))
    expect(scrollTo).toHaveBeenCalledWith(expect.objectContaining({ behavior: 'smooth' }))
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
    payload: { phase: 'build', tool: 'search_kb', args_digest: '{"query":"first"}', status: 'ok', duration_ms: 12 },
    provenance: 'harness',
    created_at: '2026-01-01T00:00:00Z',
  },
  {
    mission_id: 'm1',
    seq: 2,
    kind: 'mission.tool_call',
    payload: { phase: 'build', tool: 'shell', args_digest: '{"command":"ls"}', status: 'denied', duration_ms: 3 },
    provenance: 'harness',
    created_at: '2026-01-01T00:00:01Z',
  },
  {
    mission_id: 'm1',
    seq: 3,
    kind: 'mission.tool_call',
    payload: { phase: 'build', tool: 'write_file', args_digest: '{"path":"x"}', status: 'error', duration_ms: 40 },
    provenance: 'harness',
    created_at: '2026-01-01T00:00:02Z',
  },
  {
    mission_id: 'm1',
    seq: 4,
    kind: 'mission.turn',
    payload: { phase: 'build', duration_ms: 500, ok: true, input: 'worker_done' },
    provenance: 'harness',
    created_at: '2026-01-01T00:00:03Z',
  },
]

// openRow expands a Timeline row's own disclosure (EventLog nests a
// row's payload/children behind its own trigger), so the tool-call
// trace group inside becomes reachable.
function openRow(name: RegExp | string) {
  fireEvent.click(screen.getByRole('button', { name }))
}

describe('TimelineSection tool call trace', () => {
  it('excludes mission.tool_call events from the plain row count', () => {
    render(<TimelineSection events={toolCallTraceEvents} />)
    // Only the mission.turn row counts as an event; its three tool
    // calls are nested, not separate rows.
    expect(screen.getAllByText('1 event')).toHaveLength(1)
  })

  it('shows the tool call rows collapsed by default, one click away', () => {
    render(<TimelineSection events={toolCallTraceEvents} />)
    // Collapsed turn row already names the count; opening it reveals
    // the humanized tool call rows directly, no extra grouping click.
    expect(screen.getByText(/3 tool calls/)).toBeTruthy()
    expect(screen.queryByText('Search kb')).toBeNull()
    openRow(/Turn \(build\)/)
    expect(screen.getByText('Search kb')).toBeTruthy()
    expect(screen.getByText('Shell')).toBeTruthy()
    expect(screen.getByText('Write file')).toBeTruthy()
  })

  it('shows the raw tool name and arguments when a tool call row is expanded', () => {
    render(<TimelineSection events={toolCallTraceEvents} />)
    openRow(/Turn \(build\)/)
    fireEvent.click(screen.getByText('Search kb'))

    expect(screen.getByText('search_kb')).toBeTruthy()
    expect(screen.getByText('{"query":"first"}')).toBeTruthy()
  })

  it('collapses a tool call row again on a second click', () => {
    render(<TimelineSection events={toolCallTraceEvents} />)
    openRow(/Turn \(build\)/)
    const toggle = screen.getByText('Search kb')
    fireEvent.click(toggle)
    expect(screen.getByText('search_kb')).toBeTruthy()
    fireEvent.click(toggle)
    expect(screen.queryByText('search_kb')).toBeNull()
  })

  it('shows the hit list with title and score when a search_kb call is expanded', () => {
    const withHits: MissionEvent[] = [
      {
        mission_id: 'm1',
        seq: 1,
        kind: 'mission.tool_call',
        payload: {
          phase: 'build',
          tool: 'search_kb',
          args_digest: '{"query":"deploy"}',
          status: 'ok',
          duration_ms: 12,
          kb_hits: [{ document_id: 'doc-1', document_title: 'Runbook', score: 0.8123 }],
        },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:00Z',
      },
      {
        mission_id: 'm1',
        seq: 2,
        kind: 'mission.turn',
        payload: { phase: 'build', duration_ms: 500, ok: true, input: 'worker_done' },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:01Z',
      },
    ]
    render(<TimelineSection events={withHits} />)
    openRow(/Turn \(build\)/)
    fireEvent.click(screen.getByText('Search kb'))
    expect(screen.getByText('Runbook · score 0.8123')).toBeTruthy()
  })

  it('shows an explicit "no hits" line for a search_kb call with an empty result', () => {
    const noHits: MissionEvent[] = [
      {
        mission_id: 'm1',
        seq: 1,
        kind: 'mission.tool_call',
        payload: {
          phase: 'build',
          tool: 'search_kb',
          args_digest: '{"query":"nothing"}',
          status: 'ok',
          duration_ms: 5,
          kb_hits: [],
        },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:00Z',
      },
      {
        mission_id: 'm1',
        seq: 2,
        kind: 'mission.turn',
        payload: { phase: 'build', duration_ms: 200, ok: true, input: 'worker_done' },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:01Z',
      },
    ]
    render(<TimelineSection events={noHits} />)
    openRow(/Turn \(build\)/)
    fireEvent.click(screen.getByText('Search kb'))
    expect(screen.getByText('no hits')).toBeTruthy()
  })

  it('shows no hit list for a non-search_kb tool call', () => {
    render(<TimelineSection events={toolCallTraceEvents} />)
    openRow(/Turn \(build\)/)
    fireEvent.click(screen.getByText('Shell'))
    expect(screen.queryByText('no hits')).toBeNull()
    expect(screen.queryByText(/score/)).toBeNull()
  })

  it('shows the singular "1 tool call" label for a turn with exactly one tool call', () => {
    const oneCall: MissionEvent[] = [
      {
        mission_id: 'm1',
        seq: 1,
        kind: 'mission.tool_call',
        payload: { phase: 'build', tool: 'search_kb', args_digest: '{"query":"first"}', status: 'ok', duration_ms: 12 },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:00Z',
      },
      {
        mission_id: 'm1',
        seq: 2,
        kind: 'mission.turn',
        payload: { phase: 'build', duration_ms: 500, ok: true, input: 'worker_done' },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:01Z',
      },
    ]
    render(<TimelineSection events={oneCall} />)
    expect(screen.getByText(/1 tool call$/)).toBeTruthy()
  })

  it('shows no tool call count for a turn with no tool calls', () => {
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

describe('TimelineSection phase labels', () => {
  it('labels each row with the phase most recently started before it', () => {
    const phaseEvents: MissionEvent[] = [
      {
        mission_id: 'm1',
        seq: 1,
        kind: 'mission.phase_started',
        payload: { phase: 'discover' },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:00Z',
      },
      {
        mission_id: 'm1',
        seq: 2,
        kind: 'mission.discover_complete',
        payload: { chars: 10 },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:01Z',
      },
      {
        mission_id: 'm1',
        seq: 3,
        kind: 'mission.phase_started',
        payload: { phase: 'plan' },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:02Z',
      },
      {
        mission_id: 'm1',
        seq: 4,
        kind: 'mission.plan_created',
        payload: { units: 1 },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:03Z',
      },
    ]
    render(<TimelineSection events={phaseEvents} />)
    expect(screen.getAllByText('discover')).toHaveLength(2) // phase_started row + discover_complete row
    expect(screen.getAllByText('plan')).toHaveLength(2) // phase_started row + plan_created row
  })

  it('labels rows from their own payload phase without any phase_started', () => {
    render(<TimelineSection events={toolCallTraceEvents} />)
    expect(screen.getAllByText('build').length).toBeGreaterThan(0)
    expect(screen.getAllByText('build')[0]).toHaveClass('text-blue-700')
  })

  it('labels initial-phase rows before the first transition with their own phase', () => {
    // A mission never emits phase_started for its initial phase, so
    // discover rows must take their phase from their own payloads, not
    // from the first transition (the pre-fix bug labeled them "plan").
    const phaseEvents: MissionEvent[] = [
      {
        mission_id: 'm1',
        seq: 1,
        kind: 'mission.provisioned',
        payload: { workspace: '/w' },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:00Z',
      },
      {
        mission_id: 'm1',
        seq: 2,
        kind: 'mission.turn',
        payload: { phase: 'discover', duration_ms: 10, ok: true, input: 'phase_complete' },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:01Z',
      },
      {
        mission_id: 'm1',
        seq: 3,
        kind: 'mission.phase_started',
        payload: { phase: 'plan' },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:02Z',
      },
    ]
    render(<TimelineSection events={phaseEvents} />)
    // provisioned (backfilled) + the discover turn
    expect(screen.getAllByText('discover')).toHaveLength(2)
    expect(screen.getAllByText('plan')).toHaveLength(1)
  })

  it('survives events with a null payload (mission.resumed)', () => {
    const withNull: MissionEvent[] = [
      {
        mission_id: 'm1',
        seq: 1,
        kind: 'mission.turn',
        payload: { phase: 'build', duration_ms: 10, ok: true, input: 'worker_done' },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:00Z',
      },
      {
        mission_id: 'm1',
        seq: 2,
        kind: 'mission.resumed',
        payload: null as unknown as Record<string, unknown>,
        provenance: 'harness',
        created_at: '2026-01-01T00:00:01Z',
      },
    ]
    render(<TimelineSection events={withNull} />)
    expect(screen.getAllByText('build').length).toBeGreaterThan(0)
  })

  it('renders no phase label when no event carries a phase at all', () => {
    const bare: MissionEvent[] = [
      {
        mission_id: 'm1',
        seq: 1,
        kind: 'mission.provisioned',
        payload: { workspace: '/w' },
        provenance: 'harness',
        created_at: '2026-01-01T00:00:00Z',
      },
    ]
    render(<TimelineSection events={bare} />)
    expect(screen.queryByText('discover')).toBeNull()
  })
})
