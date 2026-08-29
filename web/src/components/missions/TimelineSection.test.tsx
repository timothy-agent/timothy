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
