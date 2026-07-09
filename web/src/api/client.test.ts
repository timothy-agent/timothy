import { describe, expect, it, vi } from 'vitest'
import { createSSEParser } from './client'
import type { ChatEvent } from './types'

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
