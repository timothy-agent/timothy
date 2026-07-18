import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  ChatError,
  chatStream,
  createSSEParser,
  listSessions,
  patchBudget,
  usageBudget,
} from './client'
import type { ChatEvent } from './types'

afterEach(() => vi.unstubAllGlobals())

describe('createSSEParser', () => {
  it('parses complete events', () => {
    const onEvent = vi.fn()
    const p = createSSEParser(onEvent)
    p.feed('data: {"type":"chunk","text":"hi"}\n\ndata: {"type":"done"}\n\n')
    p.end()

    expect(onEvent).toHaveBeenCalledTimes(2)
    expect(onEvent.mock.calls[0][0]).toEqual({ type: 'chunk', text: 'hi' })
    expect(onEvent.mock.calls[1][0]).toEqual({ type: 'done' })
  })

  it('handles events split across arbitrary chunk boundaries', () => {
    const got: ChatEvent[] = []
    const p = createSSEParser((ev) => got.push(ev))
    const wire = 'data: {"type":"chunk","text":"hello world"}\n\ndata: {"type":"meta","session_id":"s1"}\n\n'
    for (const ch of wire) p.feed(ch) // one byte at a time
    p.end()

    expect(got).toHaveLength(2)
    expect(got[0]).toEqual({ type: 'chunk', text: 'hello world' })
    expect(got[1]).toEqual({ type: 'meta', session_id: 's1' })
  })

  it('ignores comments, blank frames, and malformed JSON', () => {
    const onEvent = vi.fn()
    const p = createSSEParser(onEvent)
    p.feed(': keepalive\n\n')
    p.feed('data: {broken\n\n')
    p.feed('data: {"type":"chunk","text":"ok"}\n\n')
    p.end()

    expect(onEvent).toHaveBeenCalledTimes(1)
    expect(onEvent.mock.calls[0][0]).toEqual({ type: 'chunk', text: 'ok' })
  })

  it('flushes a trailing unterminated frame on end', () => {
    const onEvent = vi.fn()
    const p = createSSEParser(onEvent)
    p.feed('data: {"type":"chunk","text":"tail"}')
    p.end()

    expect(onEvent).toHaveBeenCalledTimes(1)
    expect(onEvent.mock.calls[0][0]).toEqual({ type: 'chunk', text: 'tail' })
  })
})

describe('chatStream errors', () => {
  it('throws a structured ChatError carrying the session id', async () => {
    vi.stubGlobal('localStorage', { getItem: () => 'tok', setItem: () => {} })
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({ error: 'chat_failed', message: 'gateway down', session_id: 's-9' }),
          { status: 502 },
        ),
      ),
    )

    const err = await chatStream({ message: 'hi' }, () => {}).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ChatError)
    const ce = err as ChatError
    expect(ce.status).toBe(502)
    expect(ce.code).toBe('chat_failed')
    expect(ce.message).toBe('gateway down')
    expect(ce.sessionId).toBe('s-9')
  })

  it('reports the session id from headers before the body streams', async () => {
    vi.stubGlobal('localStorage', { getItem: () => 'tok', setItem: () => {} })
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response('data: {"type":"done"}\n\n', {
          status: 200,
          headers: { 'X-Session-Id': 's-early' },
        }),
      ),
    )

    const sessions: string[] = []
    const events: ChatEvent[] = []
    await chatStream({ message: 'hi' }, (ev) => events.push(ev), {
      onSession: (id) => sessions.push(id),
    })

    expect(sessions).toEqual(['s-early'])
    expect(events).toEqual([{ type: 'done' }])
  })

  it('routes to the session messages endpoint when a session id is known', async () => {
    vi.stubGlobal('localStorage', { getItem: () => 'tok', setItem: () => {} })
    const fetchMock = vi.fn().mockResolvedValue(new Response('data: {"type":"done"}\n\n'))
    vi.stubGlobal('fetch', fetchMock)

    await chatStream({ session_id: 's-42', message: 'hi' }, () => {})

    expect(fetchMock.mock.calls[0][0]).toBe('/v1/sessions/s-42/messages')
    // The id travels in the path, not the body.
    expect(JSON.parse(fetchMock.mock.calls[0][1].body as string)).toEqual({ message: 'hi' })
  })

  it('pages the session list with a composite cursor', async () => {
    vi.stubGlobal('localStorage', { getItem: () => 'tok', setItem: () => {} })
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ sessions: [] }), { status: 200 }),
    )
    vi.stubGlobal('fetch', fetchMock)

    await listSessions('light', { before: '2026-07-10T12:00:00Z', beforeId: 's-42' })

    expect(fetchMock.mock.calls[0][0]).toBe(
      '/v1/sessions?query=light&before=2026-07-10T12%3A00%3A00Z&before_id=s-42',
    )
  })

  it('keeps raw text for non-json error bodies', async () => {
    vi.stubGlobal('localStorage', { getItem: () => 'tok', setItem: () => {} })
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response('plain failure', { status: 500 })),
    )

    const err = (await chatStream({ message: 'hi' }, () => {}).catch((e: unknown) => e)) as ChatError
    expect(err.status).toBe(500)
    expect(err.message).toBe('plain failure')
    expect(err.sessionId).toBeUndefined()
  })
})

describe('spend budgets', () => {
  it('fetches budget status from the usage surface', async () => {
    vi.stubGlobal('localStorage', { getItem: () => 'tok', setItem: () => {} })
    const status = {
      day: { limit_usd: 1, spend_usd: 1.5, over: true },
      month: { limit_usd: null, spend_usd: 8, over: false },
    }
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify(status), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)

    const b = await usageBudget()

    expect(fetchMock.mock.calls[0][0]).toBe('/v1/admin/usage/budget')
    expect(b).toEqual(status)
  })

  it('patches budgets keeping explicit nulls so clearing works', async () => {
    vi.stubGlobal('localStorage', { getItem: () => 'tok', setItem: () => {} })
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await patchBudget({ day: 5, month: null })

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/v1/admin/usage/budget')
    expect(init.method).toBe('PATCH')
    expect(JSON.parse(init.body as string)).toEqual({ day: 5, month: null })
  })
})
