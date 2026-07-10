import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import type { ChatEvent } from '../api/types'
import { applyEvent, type AssistantState } from '../lib/chat'
import { AssistantMessage, CompactionDivider, InterruptedMessage } from './Message'

afterEach(cleanup)

const empty: AssistantState = { text: '', reasoning: '', notices: [], streaming: true }

function play(events: ChatEvent[]): AssistantState {
  return events.reduce(applyEvent, empty)
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
