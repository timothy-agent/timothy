import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { AgentStatusLine, type AgentPhase } from './AgentStatusLine'
import { agentPhaseFromState } from '@/lib/chatUi'
import { emptyAssistant, type AssistantState } from '@/lib/chat'

describe('AgentStatusLine', () => {
  it('renders role=status with no text when idle', () => {
    render(<AgentStatusLine phase={{ kind: 'idle' }} />)
    const el = screen.getByRole('status')
    expect(el).toBeInTheDocument()
    expect(el).toHaveTextContent('')
  })

  it('renders Thinking', () => {
    render(<AgentStatusLine phase={{ kind: 'thinking' }} />)
    expect(screen.getByText('Thinking')).toBeInTheDocument()
  })

  it('renders Running <humanized label> <tool> with raw name in code', () => {
    render(<AgentStatusLine phase={{ kind: 'tool', tool: 'search_web' }} />)
    expect(screen.getByText('search_web').tagName).toBe('CODE')
    expect(screen.getAllByRole('status')[0]).toHaveTextContent('Running search web search_web')
  })

  it('renders Writing while streaming text', () => {
    render(<AgentStatusLine phase={{ kind: 'streaming' }} />)
    expect(screen.getByText('Writing')).toBeInTheDocument()
  })

  it('renders waiting for approval with a Show request button', () => {
    const onFocusRequest = vi.fn()
    render(<AgentStatusLine phase={{ kind: 'waiting', what: 'approval', onFocusRequest }} />)
    expect(screen.getByText('Waiting for your approval')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Show request' }))
    expect(onFocusRequest).toHaveBeenCalledOnce()
  })

  it('renders waiting for answer', () => {
    render(<AgentStatusLine phase={{ kind: 'waiting', what: 'answer' }} />)
    expect(screen.getByText('Waiting for your answer')).toBeInTheDocument()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('renders error first line with retry', () => {
    const onRetry = vi.fn()
    render(<AgentStatusLine phase={{ kind: 'error', message: 'boom\nmore detail', onRetry }} />)
    expect(screen.getByText('boom')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(onRetry).toHaveBeenCalledOnce()
  })

  it('renders done copy with duration and tool count', () => {
    render(<AgentStatusLine phase={{ kind: 'done', durationMs: 4200, toolCount: 3 }} />)
    expect(screen.getByText('Worked for 4.2s · 3 tools')).toBeInTheDocument()
  })

  it('renders done copy singular tool', () => {
    render(<AgentStatusLine phase={{ kind: 'done', durationMs: 500, toolCount: 1 }} />)
    expect(screen.getByText('Worked for 500ms · 1 tool')).toBeInTheDocument()
  })

  it('renders done copy with no tools and omits duration when undefined', () => {
    render(<AgentStatusLine phase={{ kind: 'done', toolCount: 0 }} />)
    expect(screen.getByText('Worked')).toBeInTheDocument()
  })
})

describe('agentPhaseFromState', () => {
  const cases: Array<[string, AssistantState | null, { pendingPermission: boolean }, AgentPhase['kind']]> = [
    ['null message', null, { pendingPermission: false }, 'idle'],
    ['non-streaming message', { ...emptyAssistant(), streaming: false }, { pendingPermission: false }, 'idle'],
    ['pending permission', emptyAssistant(), { pendingPermission: true }, 'waiting'],
    ['errored message', { ...emptyAssistant(), error: 'oops' }, { pendingPermission: false }, 'error'],
    [
      'running tool',
      { ...emptyAssistant(), tools: [{ id: '1', name: 'search_web', status: 'running' }] },
      { pendingPermission: false },
      'tool',
    ],
    ['streaming text', { ...emptyAssistant(), text: 'hello' }, { pendingPermission: false }, 'streaming'],
    ['no text no tools', emptyAssistant(), { pendingPermission: false }, 'thinking'],
  ]

  it.each(cases)('%s -> %s', (_label, msg, opts, expected) => {
    expect(agentPhaseFromState(msg, opts).kind).toBe(expected)
  })
})
