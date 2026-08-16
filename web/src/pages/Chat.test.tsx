import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ChatStreamOptions } from '../api/client'
import type { ChatEvent, ChatRequest } from '../api/types'
import type { Signal } from '../lib/events'
import { Chat } from './Chat'

vi.mock('../api/client', () => ({
  ChatError: class ChatError extends Error {
    status: number
    code?: string
    constructor(status: number, message: string, code?: string) {
      super(message)
      this.status = status
      this.code = code
    }
  },
  chatStream: vi.fn(),
  retryStream: vi.fn(),
  streamLive: vi.fn(),
  stopTurn: vi.fn(),
  getTranscript: vi.fn(),
  answerPermission: vi.fn(),
  listRoutes: vi.fn(),
  listAgents: vi.fn(),
  getSettings: vi.fn().mockResolvedValue({ settings: { transcribe_enabled: false }, values: {} }),
  listKbCollections: vi.fn().mockResolvedValue([]),
  setSessionKnowledge: vi.fn().mockResolvedValue(undefined),
}))

vi.mock('../lib/events', () => ({ subscribeEvents: vi.fn() }))

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }))

import {
  answerPermission,
  ChatError,
  chatStream,
  getTranscript,
  listAgents,
  listRoutes,
  setSessionKnowledge,
  stopTurn,
  streamLive,
} from '../api/client'
import { subscribeEvents } from '../lib/events'
import { toast } from 'sonner'

// captureSubscribe grabs the onSignal callback subscribeEvents was
// last called with, so a test can fire a "session" signal directly
// instead of driving a real SSE stream — same helper shape as
// Missions.test.tsx's own captureSubscribe.
function captureSubscribe() {
  return {
    fireSignal: (sig: Signal) => vi.mocked(subscribeEvents).mock.calls.at(-1)?.[0](sig),
  }
}

function renderChat() {
  return render(
    <MemoryRouter initialEntries={['/chat']}>
      <Routes>
        <Route path="/chat" element={<Chat onNeedToken={vi.fn()} />} />
        <Route path="/chat/:id" element={<Chat onNeedToken={vi.fn()} />} />
      </Routes>
    </MemoryRouter>,
  )
}

const routes = [
  { name: 'default', strategy: 'ordered', enabled: true, chain: [] },
  { name: 'summarize', strategy: 'price', enabled: true, chain: [] },
]

// Radix opens dropdowns on pointerdown, not click.
function openMenu(name: string) {
  const trigger = screen.getByRole('button', { name })
  fireEvent.pointerDown(trigger, { button: 0, pointerId: 1 })
  fireEvent.click(trigger)
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  // jsdom lacks scrollIntoView; the message list calls it on update.
  Element.prototype.scrollIntoView = vi.fn()
  vi.mocked(listAgents).mockResolvedValue([])
  vi.mocked(listRoutes).mockResolvedValue(routes)
  vi.mocked(subscribeEvents).mockReturnValue(vi.fn())
  vi.mocked(getTranscript).mockResolvedValue({
    session: { id: 's1', title: '', archived: false, created_at: '', updated_at: '' },
    items: [],
    turn_active: false,
  })
  vi.mocked(chatStream).mockImplementation(
    async (_req: ChatRequest, onEvent: (ev: ChatEvent) => void, opts: ChatStreamOptions = {}) => {
      opts.onSession?.('s1')
      onEvent({ type: 'meta', session_id: 's1' })
    },
  )
  vi.mocked(stopTurn).mockResolvedValue(undefined)
})

describe('Chat route picker', () => {
  it('omits route from the request body when Auto is selected', async () => {
    renderChat()
    const input = screen.getByLabelText('Message')
    fireEvent.change(input, { target: { value: 'hello' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await waitFor(() => expect(chatStream).toHaveBeenCalled())
    const [req] = vi.mocked(chatStream).mock.calls[0]
    expect(req.message).toBe('hello')
    expect(req.route).toBeUndefined()
  })

  it('includes the chosen route in the request body', async () => {
    renderChat()

    await screen.findByRole('button', { name: 'Agent and route' })
    openMenu('Agent and route')
    fireEvent.click(await screen.findByText('summarize'))

    const input = screen.getByLabelText('Message')
    fireEvent.change(input, { target: { value: 'hello' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await waitFor(() => expect(chatStream).toHaveBeenCalled())
    const [req] = vi.mocked(chatStream).mock.calls[0]
    expect(req.route).toBe('summarize')
    expect(localStorage.getItem('timothy.route')).toBe('summarize')
  })
})

describe('replayed permission asks', () => {
  it('shows the approval modal for a still-unresolved replayed ask', async () => {
    vi.mocked(getTranscript).mockResolvedValue({
      session: { id: 's1', title: '', archived: false, created_at: '', updated_at: '' },
      items: [
        { seq: 1, kind: 'user', text: 'do the thing', created_at: '' },
        {
          seq: 2,
          kind: 'permission',
          permission: {
            id: 'perm-1',
            call_id: 'call-1',
            tool: 'shell',
            args: '{}',
            danger_level: 'destructive',
            rationale: 'runs a shell command',
          },
          created_at: '',
        },
      ],
      turn_active: false,
    })

    render(
      <MemoryRouter initialEntries={['/chat/s1']}>
        <Routes>
          <Route path="/chat/:id" element={<Chat onNeedToken={vi.fn()} />} />
        </Routes>
      </MemoryRouter>,
    )

    expect(await screen.findByTestId('permission-modal')).toBeInTheDocument()
  })

  it('does not show a modal for a resolved replayed ask (server already dropped it)', async () => {
    vi.mocked(getTranscript).mockResolvedValue({
      session: { id: 's1', title: '', archived: false, created_at: '', updated_at: '' },
      items: [{ seq: 1, kind: 'user', text: 'do the thing', created_at: '' }],
      turn_active: false,
    })

    render(
      <MemoryRouter initialEntries={['/chat/s1']}>
        <Routes>
          <Route path="/chat/:id" element={<Chat onNeedToken={vi.fn()} />} />
        </Routes>
      </MemoryRouter>,
    )

    await screen.findByText('do the thing')
    expect(screen.queryByTestId('permission-modal')).not.toBeInTheDocument()
  })

  it('toasts and clears the prompt when approving a stale (404) replayed ask', async () => {
    vi.mocked(getTranscript).mockResolvedValue({
      session: { id: 's1', title: '', archived: false, created_at: '', updated_at: '' },
      items: [
        {
          seq: 1,
          kind: 'permission',
          permission: {
            id: 'perm-1',
            call_id: 'call-1',
            tool: 'shell',
            args: '{}',
            danger_level: 'safe',
            rationale: 'runs a shell command',
          },
          created_at: '',
        },
      ],
      turn_active: false,
    })
    vi.mocked(answerPermission).mockRejectedValue(new ChatError(404, 'unknown or already-answered'))

    render(
      <MemoryRouter initialEntries={['/chat/s1']}>
        <Routes>
          <Route path="/chat/:id" element={<Chat onNeedToken={vi.fn()} />} />
        </Routes>
      </MemoryRouter>,
    )

    const modal = await screen.findByTestId('permission-modal')
    fireEvent.click(within(modal).getByRole('button', { name: /Allow once/ }))

    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(screen.queryByTestId('permission-modal')).not.toBeInTheDocument()
  })
})

describe('live reattach', () => {
  it('attaches streamLive when the session is turn_active, replacing the stale interrupted item', async () => {
    vi.mocked(getTranscript).mockResolvedValue({
      session: { id: 's1', title: '', archived: false, created_at: '', updated_at: '' },
      items: [
        { seq: 1, kind: 'user', text: 'do the thing', created_at: '' },
        { seq: 2, kind: 'interrupted', text: 'partial answer so ', created_at: '' },
      ],
      turn_active: true,
    })
    // A real streamLive's promise stays pending until the stream
    // actually ends (terminal meta) — resolve it ourselves once the
    // test feeds that terminal event, matching that lifecycle instead
    // of settling immediately like a bare mock would.
    let feed!: (ev: ChatEvent) => void
    let resolveStream!: () => void
    const streamDone = new Promise<void>((r) => {
      resolveStream = r
    })
    vi.mocked(streamLive).mockImplementation(async (_id, onEvent) => {
      feed = onEvent
      await streamDone
    })

    render(
      <MemoryRouter initialEntries={['/chat/s1']}>
        <Routes>
          <Route path="/chat/:id" element={<Chat onNeedToken={vi.fn()} />} />
        </Routes>
      </MemoryRouter>,
    )

    await waitFor(() => expect(streamLive).toHaveBeenCalledWith('s1', expect.any(Function), expect.anything()))
    // The stale 'interrupted' item is gone; the replay buffer (fed
    // below) is what repopulates the text, not the persisted partial.
    expect(screen.queryByTestId('interrupted')).not.toBeInTheDocument()

    feed({ type: 'chunk', text: 'far so good' })
    await screen.findByText('far so good')

    feed({ type: 'meta', session_id: 's1' })
    resolveStream()
    // Terminal meta clears streaming — same reducer/finalization path a
    // locally-started turn goes through.
    await waitFor(() => expect(screen.queryByText('▍')).not.toBeInTheDocument())
  })

  it('does not attach live when turn_active is false', async () => {
    renderChat()
    await screen.findByRole('button', { name: 'Agent and route' })
    expect(streamLive).not.toHaveBeenCalled()
  })

  it('refetches the transcript on a session signal for the open session while unattached', async () => {
    vi.mocked(getTranscript).mockResolvedValue({
      session: { id: 's1', title: '', archived: false, created_at: '', updated_at: '' },
      items: [{ seq: 1, kind: 'user', text: 'first load', created_at: '' }],
      turn_active: false,
    })
    const { fireSignal } = captureSubscribe()

    render(
      <MemoryRouter initialEntries={['/chat/s1']}>
        <Routes>
          <Route path="/chat/:id" element={<Chat onNeedToken={vi.fn()} />} />
        </Routes>
      </MemoryRouter>,
    )
    await screen.findByText('first load')

    vi.mocked(getTranscript).mockResolvedValue({
      session: { id: 's1', title: '', archived: false, created_at: '', updated_at: '' },
      items: [
        { seq: 1, kind: 'user', text: 'first load', created_at: '' },
        { seq: 2, kind: 'user', text: 'appeared via signal', created_at: '' },
      ],
      turn_active: false,
    })
    fireSignal({ kind: 'session', id: 's1' })

    await screen.findByText('appeared via signal')
  })

  it('attaches live instead of showing an error on a 409 turn_in_flight send', async () => {
    vi.mocked(chatStream).mockRejectedValue(new ChatError(409, 'turn already in flight', 'turn_in_flight'))
    let feed!: (ev: ChatEvent) => void
    let resolveStream!: () => void
    const streamDone = new Promise<void>((r) => {
      resolveStream = r
    })
    vi.mocked(streamLive).mockImplementation(async (_id, onEvent) => {
      feed = onEvent
      await streamDone
    })

    renderChat()
    const input = screen.getByLabelText('Message')
    fireEvent.change(input, { target: { value: 'hello' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await waitFor(() => expect(toast.info).toHaveBeenCalled())
    await waitFor(() => expect(streamLive).toHaveBeenCalled())
    // No generic failure state rendered — the in-flight turn's own
    // stream (fed below) is what populates the answer.
    expect(screen.queryByText(/turn already in flight/)).not.toBeInTheDocument()

    feed({ type: 'chunk', text: 'the other turn wins' })
    await screen.findByText('the other turn wins')

    feed({ type: 'meta', session_id: 's1' })
    resolveStream()
    await waitFor(() => expect(screen.queryByText('▍')).not.toBeInTheDocument())
  })

  it('reattaches via streamLive instead of showing an error on a dropped connection', async () => {
    // A plain TypeError (network cut, proxy timeout) — not a ChatError,
    // so there's no definite server response to treat as a real failure.
    // Needs an already-adopted session (sessionRef populated from the
    // route param), since a transport failure carries no session_id to
    // adopt the way a ChatError with err.sessionId would.
    vi.mocked(chatStream).mockRejectedValue(new TypeError('Failed to fetch'))
    let feed!: (ev: ChatEvent) => void
    let resolveStream!: () => void
    const streamDone = new Promise<void>((r) => {
      resolveStream = r
    })
    vi.mocked(streamLive).mockImplementation(async (_id, onEvent) => {
      feed = onEvent
      await streamDone
    })

    render(
      <MemoryRouter initialEntries={['/chat/s1']}>
        <Routes>
          <Route path="/chat/:id" element={<Chat onNeedToken={vi.fn()} />} />
        </Routes>
      </MemoryRouter>,
    )

    const input = await screen.findByLabelText('Message')
    fireEvent.change(input, { target: { value: 'hello' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await waitFor(() => expect(toast.info).toHaveBeenCalledWith('Connection dropped, reattaching'))
    await waitFor(() => expect(streamLive).toHaveBeenCalledWith('s1', expect.any(Function), expect.anything()))
    expect(screen.queryByText(/Failed to fetch/)).not.toBeInTheDocument()

    feed({ type: 'chunk', text: 'reattached and still going' })
    await screen.findByText('reattached and still going')

    feed({ type: 'meta', session_id: 's1' })
    resolveStream()
    await waitFor(() => expect(screen.queryByText('▍')).not.toBeInTheDocument())
  })

  it('ignores a session signal for a different session', async () => {
    vi.mocked(getTranscript).mockResolvedValue({
      session: { id: 's1', title: '', archived: false, created_at: '', updated_at: '' },
      items: [{ seq: 1, kind: 'user', text: 'first load', created_at: '' }],
      turn_active: false,
    })
    const { fireSignal } = captureSubscribe()

    render(
      <MemoryRouter initialEntries={['/chat/s1']}>
        <Routes>
          <Route path="/chat/:id" element={<Chat onNeedToken={vi.fn()} />} />
        </Routes>
      </MemoryRouter>,
    )
    await screen.findByText('first load')
    vi.mocked(getTranscript).mockClear()

    fireSignal({ kind: 'session', id: 'some-other-session' })
    await new Promise((r) => setTimeout(r, 10))
    expect(getTranscript).not.toHaveBeenCalled()
  })
})

describe('stop turn', () => {
  it('calls stopTurn when the Stop button is clicked mid-stream', async () => {
    // A chatStream that never resolves on its own — the turn is still
    // "streaming" from the page's point of view until the test clicks
    // Stop, mirroring a real in-flight turn.
    let released!: () => void
    vi.mocked(chatStream).mockImplementation(
      async (_req: ChatRequest, onEvent: (ev: ChatEvent) => void, opts: ChatStreamOptions = {}) => {
        opts.onSession?.('s1')
        await new Promise<void>((r) => {
          released = r
        })
        onEvent({ type: 'meta', session_id: 's1' })
      },
    )

    renderChat()
    const input = screen.getByLabelText('Message')
    fireEvent.change(input, { target: { value: 'hello' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    const stopButton = await screen.findByRole('button', { name: 'Stop' })
    fireEvent.click(stopButton)

    expect(stopTurn).toHaveBeenCalledWith('s1')
    released() // let the pending chatStream settle so the test can end cleanly
  })

  it('does NOT call stopTurn on unmount — only the local fetch is aborted', async () => {
    let released!: () => void
    vi.mocked(chatStream).mockImplementation(
      async (_req: ChatRequest, onEvent: (ev: ChatEvent) => void, opts: ChatStreamOptions = {}) => {
        opts.onSession?.('s1')
        await new Promise<void>((r) => {
          released = r
        })
        onEvent({ type: 'meta', session_id: 's1' })
      },
    )

    const { unmount } = renderChat()
    const input = screen.getByLabelText('Message')
    fireEvent.change(input, { target: { value: 'hello' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await screen.findByRole('button', { name: 'Stop' })
    unmount()

    expect(stopTurn).not.toHaveBeenCalled()
    released()
  })
})

describe('session knowledge', () => {
  it('seeds chips from the session and includes them in the send request', async () => {
    vi.mocked(getTranscript).mockResolvedValue({
      session: {
        id: 's1',
        title: '',
        archived: false,
        knowledge: ['observability'],
        created_at: '',
        updated_at: '',
      },
      items: [],
      turn_active: false,
    })

    render(
      <MemoryRouter initialEntries={['/chat/s1']}>
        <Routes>
          <Route path="/chat/:id" element={<Chat onNeedToken={vi.fn()} />} />
        </Routes>
      </MemoryRouter>,
    )
    await screen.findByText('#observability')

    const input = screen.getByLabelText('Message')
    fireEvent.change(input, { target: { value: 'hello' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    await waitFor(() =>
      expect(chatStream).toHaveBeenCalledWith(
        expect.objectContaining({ knowledge: ['observability'] }),
        expect.anything(),
        expect.anything(),
      ),
    )
  })

  it('removing a chip calls setSessionKnowledge with the remaining names', async () => {
    vi.mocked(getTranscript).mockResolvedValue({
      session: {
        id: 's1',
        title: '',
        archived: false,
        knowledge: ['observability', 'billing'],
        created_at: '',
        updated_at: '',
      },
      items: [],
      turn_active: false,
    })

    render(
      <MemoryRouter initialEntries={['/chat/s1']}>
        <Routes>
          <Route path="/chat/:id" element={<Chat onNeedToken={vi.fn()} />} />
        </Routes>
      </MemoryRouter>,
    )
    await screen.findByText('#observability')

    fireEvent.click(screen.getByRole('button', { name: 'Remove observability knowledge' }))

    await waitFor(() =>
      expect(setSessionKnowledge).toHaveBeenCalledWith('s1', ['billing']),
    )
    expect(screen.queryByText('#observability')).toBeNull()
  })

  it('carries knowledge from a home-screen intent into the first send', async () => {
    render(
      <MemoryRouter
        initialEntries={[{ pathname: '/chat', state: { send: 'hello', knowledge: ['observability'] } }]}
      >
        <Routes>
          <Route path="/chat" element={<Chat onNeedToken={vi.fn()} />} />
          <Route path="/chat/:id" element={<Chat onNeedToken={vi.fn()} />} />
        </Routes>
      </MemoryRouter>,
    )

    await waitFor(() =>
      expect(chatStream).toHaveBeenCalledWith(
        expect.objectContaining({ message: 'hello', knowledge: ['observability'] }),
        expect.anything(),
        expect.anything(),
      ),
    )
    await screen.findByText('#observability')
  })

  it('shows the serving agent\'s bound collections as muted chips, updating on agent switch', async () => {
    vi.mocked(listAgents).mockResolvedValue([
      {
        id: 'a1',
        name: 'general',
        description: '',
        prompt_overlay: '',
        route: '',
        skills: [],
        tools: [],
        memory: true,
        is_default: true,
        enabled: true,
        knowledge: ['runbooks'],
      },
      {
        id: 'a2',
        name: 'research',
        description: '',
        prompt_overlay: '',
        route: '',
        skills: [],
        tools: [],
        memory: true,
        is_default: false,
        enabled: true,
        knowledge: ['papers'],
      },
    ])

    renderChat()
    await screen.findByText('#runbooks')
    expect(screen.queryByRole('button', { name: 'Remove runbooks knowledge' })).toBeNull()
    expect(screen.queryByText('#papers')).toBeNull()

    openMenu('Agent and route')
    fireEvent.click(await screen.findByText('research'))

    await screen.findByText('#papers')
    expect(screen.queryByText('#runbooks')).toBeNull()
  })

  it('toasts and restores the chip when setSessionKnowledge fails', async () => {
    vi.mocked(getTranscript).mockResolvedValue({
      session: {
        id: 's1',
        title: '',
        archived: false,
        knowledge: ['observability'],
        created_at: '',
        updated_at: '',
      },
      items: [],
      turn_active: false,
    })
    vi.mocked(setSessionKnowledge).mockRejectedValueOnce(new Error('boom'))

    render(
      <MemoryRouter initialEntries={['/chat/s1']}>
        <Routes>
          <Route path="/chat/:id" element={<Chat onNeedToken={vi.fn()} />} />
        </Routes>
      </MemoryRouter>,
    )
    await screen.findByText('#observability')

    fireEvent.click(screen.getByRole('button', { name: 'Remove observability knowledge' }))

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Could not update knowledge'))
    await screen.findByText('#observability')
  })
})
