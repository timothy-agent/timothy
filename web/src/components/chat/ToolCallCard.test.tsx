import type { ReactNode } from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ToolCallCard, ToolCallGroup } from './ToolCallCard'
import { toolCategory, humanizeTool } from '@/lib/chatUi'
import type { ToolRun } from '@/lib/chat'

function renderWithProvider(children: ReactNode) {
  return render(<TooltipProvider>{children}</TooltipProvider>)
}

describe('toolCategory', () => {
  const cases: Array<[string, string]> = [
    ['read_file', 'file'],
    ['write_file', 'file'],
    ['edit', 'file'],
    ['shell', 'shell'],
    ['run_command', 'shell'],
    ['bash', 'shell'],
    ['search_web', 'web'],
    ['fetch_url', 'web'],
    ['search_mail', 'mail'],
    ['list_calendar_events', 'calendar'],
    ['memory_recall', 'memory'],
    ['kb_search', 'kb'],
    ['mcp_fetch', 'connector'],
    ['unknown_thing', 'connector'],
    ['plaintool', 'other'],
  ]
  it.each(cases)('%s -> %s', (name, expected) => {
    expect(toolCategory(name)).toBe(expected)
  })
})

describe('humanizeTool', () => {
  it('extracts path', () => {
    const run: ToolRun = { id: '1', name: 'read_file', status: 'ok', args: JSON.stringify({ path: 'src/loop.go' }) }
    expect(humanizeTool(run)).toEqual({ action: 'Read file', target: 'src/loop.go' })
  })

  it('extracts command', () => {
    const run: ToolRun = { id: '1', name: 'shell', status: 'ok', args: JSON.stringify({ command: 'go test ./...' }) }
    expect(humanizeTool(run)).toEqual({ action: 'Shell', target: 'go test ./...' })
  })

  it('extracts query', () => {
    const run: ToolRun = { id: '1', name: 'search_web', status: 'ok', args: JSON.stringify({ query: 'pgvector rrf' }) }
    expect(humanizeTool(run)).toEqual({ action: 'Search web', target: 'pgvector rrf' })
  })

  it('has no target when args missing', () => {
    const run: ToolRun = { id: '1', name: 'search_web', status: 'ok' }
    expect(humanizeTool(run)).toEqual({ action: 'Search web' })
  })

  it('has no target when args unparsable', () => {
    const run: ToolRun = { id: '1', name: 'search_web', status: 'ok', args: 'not json' }
    expect(humanizeTool(run)).toEqual({ action: 'Search web' })
  })
})

describe('ToolCallCard', () => {
  it('shows collapsed action, mono target, and duration', () => {
    const run: ToolRun = {
      id: '1',
      name: 'read_file',
      status: 'ok',
      args: JSON.stringify({ path: 'src/loop.go' }),
      durationMs: 12,
    }
    render(<ToolCallCard run={run} />)
    expect(screen.getByText('Read file')).toBeInTheDocument()
    expect(screen.getByText('src/loop.go')).toBeInTheDocument()
    expect(screen.getByText('12ms')).toBeInTheDocument()
  })

  it('expands to show Arguments JSON on click', () => {
    const run: ToolRun = {
      id: '1',
      name: 'read_file',
      status: 'ok',
      args: JSON.stringify({ path: 'src/loop.go' }),
    }
    renderWithProvider(<ToolCallCard run={run} />)
    fireEvent.click(screen.getByRole('button'))
    expect(screen.getByLabelText('Arguments')).toBeInTheDocument()
  })

  it('gives an error digest the destructive class', () => {
    const run: ToolRun = { id: '1', name: 'shell', status: 'error', digest: 'exit 1' }
    render(<ToolCallCard run={run} defaultOpen />)
    fireEvent.click(screen.getByRole('button'))
    const pre = screen.getByText('exit 1')
    expect(pre).toHaveClass('text-destructive')
  })

  it('shows a denied line for denied status', () => {
    const run: ToolRun = { id: '1', name: 'shell', status: 'denied' }
    render(<ToolCallCard run={run} />)
    fireEvent.click(screen.getByRole('button'))
    expect(screen.getByText('Denied by you')).toBeInTheDocument()
  })

  it('shows unparsable args as the raw string', () => {
    const run: ToolRun = { id: '1', name: 'shell', status: 'ok', args: 'not json' }
    renderWithProvider(<ToolCallCard run={run} />)
    fireEvent.click(screen.getByRole('button'))
    expect(screen.getByLabelText('Arguments')).toHaveTextContent('not json')
  })

  it('truncates a long digest with a "Show all" toggle that expands and collapses', () => {
    const lines = Array.from({ length: 25 }, (_, i) => `line ${i}`)
    const run: ToolRun = { id: '1', name: 'shell', status: 'ok', digest: lines.join('\n') }
    render(<ToolCallCard run={run} defaultOpen />)
    fireEvent.click(screen.getByRole('button', { name: /Shell/ }))
    expect(screen.getByText('line 0', { exact: false })).toBeInTheDocument()
    expect(screen.queryByText('line 24', { exact: false })).not.toBeInTheDocument()
    const toggle = screen.getByRole('button', { name: 'Show all (+5 lines)' })
    fireEvent.click(toggle)
    expect(screen.getByText('Show less')).toBeInTheDocument()
    expect(screen.getByText('line 24', { exact: false })).toBeInTheDocument()
    fireEvent.click(screen.getByText('Show less'))
    expect(screen.getByText('Show all (+5 lines)')).toBeInTheDocument()
  })
})

describe('ToolCallGroup', () => {
  it('renders a single run without group chrome', () => {
    const runs: ToolRun[] = [{ id: '1', name: 'search_web', status: 'ok' }]
    render(<ToolCallGroup runs={runs} />)
    expect(screen.queryByText('1 tool calls')).not.toBeInTheDocument()
    expect(screen.getByText('Search web')).toBeInTheDocument()
  })

  it('groups multiple runs with a summary and total duration', () => {
    const runs: ToolRun[] = [
      { id: '1', name: 'search_web', status: 'ok', durationMs: 640 },
      { id: '2', name: 'read_file', status: 'ok', durationMs: 12 },
    ]
    render(<ToolCallGroup runs={runs} />)
    expect(screen.getByText('2 tool calls')).toBeInTheDocument()
    expect(screen.getByText('652ms')).toBeInTheDocument()
  })

  it('expands to individual cards', () => {
    const runs: ToolRun[] = [
      { id: '1', name: 'search_web', status: 'ok' },
      { id: '2', name: 'read_file', status: 'ok' },
    ]
    render(<ToolCallGroup runs={runs} />)
    fireEvent.click(screen.getByText('2 tool calls'))
    expect(screen.getByText('Search web')).toBeInTheDocument()
    expect(screen.getByText('Read file')).toBeInTheDocument()
  })

  it('hides step notes while collapsed and shows them when expanded', () => {
    const runs: ToolRun[] = [
      { id: '1', name: 'search_web', status: 'ok' },
      { id: '2', name: 'read_file', status: 'ok' },
    ]
    const notes = ['Checking your notes for tone.', 'Loading the writing skill.']
    render(<ToolCallGroup runs={runs} stepNotes={notes} />)

    expect(screen.queryByText(notes[0])).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('2 tool calls'))
    expect(screen.getByText(notes[0])).toBeInTheDocument()
    expect(screen.getByText(notes[1])).toBeInTheDocument()
  })

  it('keeps group chrome for a single run that carries step notes', () => {
    const runs: ToolRun[] = [{ id: '1', name: 'search_web', status: 'ok' }]
    render(<ToolCallGroup runs={runs} stepNotes={['Looking that up.']} />)

    fireEvent.click(screen.getByText('1 tool call'))
    expect(screen.getByText('Looking that up.')).toBeInTheDocument()
  })
})
