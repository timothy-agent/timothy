import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ChatEvent } from '../api/types'
import { applyEvent, emptyAssistant, type AssistantState } from '../lib/chat'
import { AssistantMessage, CompactionDivider, InterruptedMessage, UserMessage } from './Message'

vi.mock('../api/client', () => ({
  fetchAttachmentBlob: vi.fn(),
}))

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

  it('shows a reasoning-only activity line that opens the detail panel', () => {
    const msg = play([
      { type: 'reasoning_chunk', text: 'thinking…' },
      { type: 'chunk', text: 'answer' },
      { type: 'meta', session_id: 's' },
    ])
    const onShowActivity = vi.fn()
    render(<AssistantMessage msg={msg} onShowActivity={onShowActivity} />)

    const line = screen.getByTestId('activity-line')
    expect(line).toHaveTextContent('Reasoning')
    fireEvent.click(line)
    expect(onShowActivity).toHaveBeenCalledOnce()
  })

  it('omits the activity line entirely when there is no onShowActivity handler', () => {
    const msg = play([
      { type: 'reasoning_chunk', text: 'thinking…' },
      { type: 'chunk', text: 'answer' },
      { type: 'meta', session_id: 's' },
    ])
    render(<AssistantMessage msg={msg} />)
    expect(screen.queryByTestId('activity-line')).not.toBeInTheDocument()
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

  it('shows a retry button only when onRetry is given, and calls it on click', () => {
    const msg = play([
      { type: 'error', error: { code: 'chain_exhausted', message: 'all failed', retryable: false } },
      { type: 'meta', session_id: 's' },
    ])
    const { rerender } = render(<AssistantMessage msg={msg} />)
    expect(screen.queryByTestId('retry-button')).not.toBeInTheDocument()

    const onRetry = vi.fn()
    rerender(<AssistantMessage msg={msg} onRetry={onRetry} />)
    fireEvent.click(screen.getByTestId('retry-button'))
    expect(onRetry).toHaveBeenCalledOnce()
  })

  it('omits the retry button when there is no error, even with onRetry given', () => {
    const msg = play([{ type: 'chunk', text: 'all good' }, { type: 'meta', session_id: 's' }])
    render(<AssistantMessage msg={msg} onRetry={vi.fn()} />)
    expect(screen.queryByTestId('retry-button')).not.toBeInTheDocument()
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

  it('renders no retry button on a user message without onRetry', () => {
    render(<UserMessage text="my question" />)
    expect(screen.queryByTestId('retry-button')).not.toBeInTheDocument()
  })

  it('shows a retry button on a user message when onRetry is given, and calls it on click', () => {
    const onRetry = vi.fn()
    render(<UserMessage text="my question" onRetry={onRetry} />)
    fireEvent.click(screen.getByTestId('retry-button'))
    expect(onRetry).toHaveBeenCalledOnce()
  })

  it('renders markdown in a user message', () => {
    render(<UserMessage text={'**bold** and `code`'} />)
    expect(screen.getByText('bold').tagName).toBe('STRONG')
    expect(screen.getByText('code').tagName).toBe('CODE')
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

    render(<AssistantMessage msg={msg} onShowActivity={vi.fn()} />)
    expect(screen.getByTestId('activity-line')).toHaveTextContent('Worked for 42ms · shell')
  })

  it('shows a running tool while streaming', () => {
    const msg = play([{ type: 'tool_start', tool_call: { id: 'c1', name: 'web_fetch' } }])
    render(<AssistantMessage msg={msg} onShowActivity={vi.fn()} />)
    expect(screen.getByTestId('activity-line')).toHaveTextContent('Running web_fetch…')
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

describe('duration badge', () => {
  it('renders a duration pill beside the existing meta pills', () => {
    const msg = play([
      { type: 'chunk', text: 'hi' },
      {
        type: 'meta',
        session_id: 's',
        provider: 'zai-glm',
        model: 'glm-4.7',
        usage: { input_tokens: 11, output_tokens: 204 },
        duration_ms: 81_000,
      },
    ])
    render(<AssistantMessage msg={msg} />)

    const badge = screen.getByTestId('meta-badge')
    expect(badge).toHaveTextContent('zai-glm')
    expect(badge).toHaveTextContent('glm-4.7')
    expect(badge).toHaveTextContent('11→204 tok')
    expect(screen.getByTestId('duration-badge')).toHaveTextContent('1m 21s')
  })

  it('renders sub-minute durations via formatDuration', () => {
    const msg = play([
      { type: 'chunk', text: 'hi' },
      { type: 'meta', session_id: 's', provider: 'zai-glm', model: 'glm-4.7', duration_ms: 6700 },
    ])
    render(<AssistantMessage msg={msg} />)
    expect(screen.getByTestId('duration-badge')).toHaveTextContent('6.7s')
  })

  it('omits the duration pill when duration_ms is absent (old turns)', () => {
    const msg = play([
      { type: 'chunk', text: 'hi' },
      { type: 'meta', session_id: 's', provider: 'zai-glm', model: 'glm-4.7' },
    ])
    render(<AssistantMessage msg={msg} />)
    expect(screen.queryByTestId('duration-badge')).not.toBeInTheDocument()
  })
})

describe('UserMessage attachments', () => {
  beforeEach(() => {
    vi.stubGlobal('URL', {
      ...URL,
      createObjectURL: vi.fn(() => 'blob:mock-fetched'),
      revokeObjectURL: vi.fn(),
    })
  })

  it('renders an AuthedImage per attached image, fetched through the authed client', async () => {
    const client = await import('../api/client')
    vi.mocked(client.fetchAttachmentBlob).mockResolvedValue(new Blob(['x'], { type: 'image/png' }))

    render(
      <UserMessage
        text="check this out"
        images={[
          { id: 'att-1', mime: 'image/png' },
          { id: 'att-2', mime: 'image/jpeg' },
        ]}
      />,
    )

    await waitFor(() => expect(client.fetchAttachmentBlob).toHaveBeenCalledWith('att-1'))
    expect(client.fetchAttachmentBlob).toHaveBeenCalledWith('att-2')
    await waitFor(() => expect(screen.getAllByRole('img')).toHaveLength(2))
  })

  it('uses the optimistic local object URL directly, skipping the fetch', () => {
    const localUrls = new Map([['att-1', 'blob:local-preview']])
    render(
      <UserMessage text="sending…" images={[{ id: 'att-1', mime: 'image/png' }]} localUrls={localUrls} />,
    )
    const img = screen.getByRole('img') as HTMLImageElement
    expect(img.src).toBe('blob:local-preview')
  })

  it('shows a broken-image fallback when the fetch fails', async () => {
    const client = await import('../api/client')
    vi.mocked(client.fetchAttachmentBlob).mockRejectedValue(new Error('404'))

    render(<UserMessage text="oops" images={[{ id: 'att-missing', mime: 'image/png' }]} />)

    await waitFor(() => expect(screen.getByTestId('attachment-error')).toBeInTheDocument())
  })

  it('renders no image grid when the message carries no images', () => {
    render(<UserMessage text="plain text" />)
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
  })

  it('renders a non-clickable chip per attached document, never fetching it', async () => {
    const client = await import('../api/client')

    render(
      <UserMessage
        text="summarize this"
        documents={[{ id: 'doc-1', mime: 'application/pdf' }]}
      />,
    )

    expect(screen.getByText('PDF')).toBeInTheDocument()
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    expect(vi.mocked(client.fetchAttachmentBlob).mock.calls.flat()).not.toContain('doc-1')
  })

  it('renders no document chips when the message carries none', () => {
    render(<UserMessage text="plain text" />)
    expect(screen.queryByText('PDF')).not.toBeInTheDocument()
  })
})
