import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ChatEvent } from '../api/types'
import { applyEvent, emptyAssistant, type AssistantState } from '../lib/chat'
import {
  AssistantMessage,
  CompactionDivider,
  InterruptedMessage,
  ToolBlock,
  UserMessage,
} from './Message'

afterEach(cleanup)

function play(events: ChatEvent[]): AssistantState {
  return events.reduce(applyEvent, emptyAssistant())
}

describe('AssistantMessage', () => {
  it('renders accumulated chunks as markdown', () => {
    const msg = play([
      { type: 'chunk', text: 'Hello ' },
      { type: 'chunk', text: '**world**' },
      { type: 'meta', session_id: 's' },
    ])
    render(<AssistantMessage msg={msg} />)

    expect(screen.getByText('world')).toBeInTheDocument()
    expect(screen.getByText('world').tagName).toBe('STRONG')
  })

  it('collapses reasoning behind a toggle', () => {
    const msg = play([
      { type: 'reasoning_chunk', text: 'thinking…' },
      { type: 'chunk', text: 'answer' },
      { type: 'meta', session_id: 's' },
    ])
    render(<AssistantMessage msg={msg} />)

    const details = screen.getByText('Reasoning').closest('details')
    expect(details).not.toBeNull()
    expect(details).not.toHaveAttribute('open')
    expect(screen.getByText('thinking…')).toBeInTheDocument()
  })

  it('renders retry and incomplete honestly', () => {
    const msg = play([
      { type: 'retry', retry: { attempt: 1, backoff_ms: 500, reason: 'http 429' } },
      { type: 'chunk', text: 'partial' },
      { type: 'incomplete', text: 'stream ended before completion' },
      { type: 'meta', session_id: 's' },
    ])
    render(<AssistantMessage msg={msg} />)

    const notices = screen.getAllByTestId('notice')
    expect(notices).toHaveLength(2)
    expect(notices[0]).toHaveTextContent('retrying (attempt 1): http 429')
    expect(notices[1]).toHaveTextContent('incomplete')
  })

  it('shows provider, model, and token badge from meta', () => {
    const msg = play([
      { type: 'chunk', text: 'hi' },
      {
        type: 'meta',
        session_id: 's',
        provider: 'zai-glm',
        model: 'glm-4.7',
        usage: { input_tokens: 11, output_tokens: 204 },
      },
    ])
    render(<AssistantMessage msg={msg} />)

    const badge = screen.getByTestId('meta-badge')
    expect(badge).toHaveTextContent('zai-glm')
    expect(badge).toHaveTextContent('glm-4.7')
    expect(badge).toHaveTextContent('11→204 tok')
  })

  it('renders errors as errors', () => {
    const msg = play([
      { type: 'error', error: { code: 'chain_exhausted', message: 'all failed', retryable: false } },
      { type: 'meta', session_id: 's' },
    ])
    render(<AssistantMessage msg={msg} />)

    expect(screen.getByTestId('error')).toHaveTextContent('chain_exhausted: all failed')
  })
})

describe('replay-only components', () => {
  it('renders the compaction divider text', () => {
    render(<CompactionDivider text="older messages summarized (through #4)" />)
    expect(screen.getByTestId('compaction-divider')).toHaveTextContent(
      'older messages summarized (through #4)',
    )
  })

  it('renders an interrupted turn: the partial plus an honest marker', () => {
    render(<InterruptedMessage text="Once upon a" />)
    const el = screen.getByTestId('interrupted')
    expect(el).toHaveTextContent('Once upon a')
    expect(el).toHaveTextContent('interrupted')
  })
})

describe('copy buttons', () => {
  it('copies the assistant reply text', () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })

    const msg = play([
      { type: 'chunk', text: 'the answer' },
      { type: 'meta', session_id: 's' },
    ])
    render(<AssistantMessage msg={msg} />)

    fireEvent.click(screen.getByTestId('copy-button'))
    expect(writeText).toHaveBeenCalledWith('the answer')
    vi.unstubAllGlobals()
  })

  it('copies the user message text', () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })

    render(<UserMessage text="my question" />)
    fireEvent.click(screen.getByTestId('copy-button'))
    expect(writeText).toHaveBeenCalledWith('my question')
    vi.unstubAllGlobals()
  })

  it('hides the copy button while streaming', () => {
    const msg = play([{ type: 'chunk', text: 'partial' }])
    render(<AssistantMessage msg={msg} />)
    expect(screen.queryByTestId('copy-button')).toBeNull()
  })

  it('copies the interrupted partial text', () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })

    render(<InterruptedMessage text="Once upon a" />)
    fireEvent.click(screen.getByTestId('copy-button'))
    expect(writeText).toHaveBeenCalledWith('Once upon a')
    vi.unstubAllGlobals()
  })

  it('confirms the copy then reverts after two seconds', async () => {
    vi.useFakeTimers()
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })

    render(<UserMessage text="hi" />)
    const btn = screen.getByTestId('copy-button')
    fireEvent.click(btn)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(btn).toHaveAttribute('data-copied', 'true')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    expect(btn).toHaveAttribute('data-copied', 'false')
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('stays quiet when the clipboard rejects', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('denied'))
    vi.stubGlobal('navigator', { clipboard: { writeText } })

    render(<UserMessage text="hi" />)
    const btn = screen.getByTestId('copy-button')
    fireEvent.click(btn)
    await act(async () => {
      await Promise.resolve()
    })
    expect(btn).toHaveAttribute('data-copied', 'false')
    vi.unstubAllGlobals()
  })

  it('renders no footer for a finished turn with no text and no meta', () => {
    const msg = play([
      { type: 'error', error: { code: 'chain_exhausted', message: 'all failed', retryable: false } },
      { type: 'meta', session_id: 's' },
    ])
    render(<AssistantMessage msg={msg} />)
    expect(screen.queryByTestId('copy-button')).toBeNull()
    expect(screen.queryByTestId('meta-badge')).toBeNull()
  })
})

describe('tool calls', () => {
  it('tracks a tool call through start, end, and result', () => {
    const msg = play([
      { type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } },
      { type: 'tool_end', tool_call: { id: 'c1', name: 'shell', input: { command: 'ls' } } },
      {
        type: 'tool_result',
        tool_result: { id: 'c1', name: 'shell', status: 'ok', digest: 'notes.md', duration_ms: 42 },
      },
      { type: 'chunk', text: 'you have one file' },
      { type: 'meta', session_id: 's' },
    ])
    expect(msg.tools).toHaveLength(1)
    expect(msg.tools[0]).toMatchObject({ id: 'c1', status: 'ok', digest: 'notes.md' })

    render(<AssistantMessage msg={msg} />)
    expect(screen.getByTestId('tool-block')).toHaveTextContent('shell')
    expect(screen.getByTestId('tool-status')).toHaveTextContent('ok')
    expect(screen.getByTestId('tool-block')).toHaveTextContent('42ms')
  })

  it('shows a running tool before its result arrives', () => {
    const msg = play([{ type: 'tool_start', tool_call: { id: 'c1', name: 'web_fetch' } }])
    render(<AssistantMessage msg={msg} />)
    expect(screen.getByTestId('tool-status')).toHaveTextContent('running…')
  })

  it('renders a standalone replayed tool block', () => {
    render(
      <ToolBlock
        tool={{ id: 'c9', name: 'calculate', status: 'error', digest: 'division by zero' }}
      />,
    )
    expect(screen.getByTestId('tool-status')).toHaveTextContent('error')
  })

  it('parks visibly while a permission request is pending', () => {
    const msg = play([
      { type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } },
      {
        type: 'permission_request',
        permission: {
          id: 'p1',
          call_id: 'c1',
          tool: 'shell',
          args: '{"command":"rm x"}',
          danger_level: 'destructive',
          rationale: 'destructive command pattern: rm',
        },
      },
    ])
    expect(msg.permissions).toHaveLength(1)
    render(<AssistantMessage msg={msg} />)
    expect(screen.getByTestId('awaiting-approval')).toHaveTextContent('waiting for your approval')
  })

  it('clears pending permissions when the tool result lands', () => {
    const msg = play([
      { type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } },
      {
        type: 'permission_request',
        permission: {
          id: 'p1',
          call_id: 'c1',
          tool: 'shell',
          args: '{}',
          danger_level: 'safe',
          rationale: 'no standing grant',
        },
      },
      {
        type: 'tool_result',
        tool_result: { id: 'c1', name: 'shell', status: 'ok', duration_ms: 5 },
      },
    ])
    expect(msg.permissions).toHaveLength(0)
  })

  it('keeps a second parallel prompt when the first call resolves', () => {
    const mkPrompt = (pid: string, cid: string): ChatEvent => ({
      type: 'permission_request',
      permission: {
        id: pid,
        call_id: cid,
        tool: 'shell',
        args: '{}',
        danger_level: 'destructive',
        rationale: 'destructive command pattern: rm',
      },
    })
    const msg = play([
      { type: 'tool_start', tool_call: { id: 'c1', name: 'shell' } },
      { type: 'tool_start', tool_call: { id: 'c2', name: 'shell' } },
      mkPrompt('p1', 'c1'),
      mkPrompt('p2', 'c2'),
      // Answering call c1 resolves ONLY its prompt; c2 stays parked.
      { type: 'tool_result', tool_result: { id: 'c1', name: 'shell', status: 'ok', duration_ms: 3 } },
    ])
    expect(msg.permissions).toHaveLength(1)
    expect(msg.permissions[0].call_id).toBe('c2')
  })
})
