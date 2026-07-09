import { afterEach, describe, expect, it, vi } from 'vitest'
import { ChatError, chatStream, createSSEParser } from './client'
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
