import { describe, expect, it } from 'vitest'
import type { TranscriptItem } from '../api/types'
import { fromTranscript } from './transcript'

const at = '2026-07-10T12:00:00Z'

describe('fromTranscript', () => {
  it('replays a full session shape: turns, compaction, interruption', () => {
    const fixture: TranscriptItem[] = [
      { seq: 2, kind: 'user', text: 'hello', created_at: at },
      {
        seq: 3,
        kind: 'assistant',
        blocks: [
          { type: 'reasoning', text: 'thinking about it' },
          { type: 'text', text: 'hi **there**' },
        ],
        provider: 'zai-glm',
        model: 'glm-4.7',
        usage: { input_tokens: 81, output_tokens: 396 },
        created_at: at,
      },
      { seq: 4, kind: 'compaction', text: 'older messages summarized (through #3)', created_at: at },
      { seq: 5, kind: 'user', text: 'go on', created_at: at },
      { seq: 6, kind: 'interrupted', text: 'Once upon a', created_at: at },
    ]

    const items = fromTranscript(fixture)

    expect(items.map((i) => i.role)).toEqual([
      'user',
      'assistant',
      'compaction',
      'user',
      'interrupted',
    ])
    expect(items[0]).toMatchObject({ role: 'user', text: 'hello' })
    expect(items[1]).toMatchObject({
      role: 'assistant',
      text: 'hi **there**',
      reasoning: 'thinking about it',
      tools: [],
      streaming: false,
      meta: { provider: 'zai-glm', model: 'glm-4.7', usage: { input_tokens: 81, output_tokens: 396 } },
    })
    expect(items[2]).toMatchObject({ role: 'compaction', text: 'older messages summarized (through #3)' })
    expect(items[4]).toMatchObject({ role: 'interrupted', text: 'Once upon a' })
    // Stable ids keyed by seq so React reconciles replays predictably.
    expect(items.map((i) => i.id)).toEqual(['replay-2', 'replay-3', 'replay-4', 'replay-5', 'replay-6'])
  })

  it('concatenates multiple blocks of the same type', () => {
    const items = fromTranscript([
      {
        seq: 1,
        kind: 'assistant',
        blocks: [
          { type: 'text', text: 'part one, ' },
          { type: 'text', text: 'part two' },
        ],
        created_at: at,
      },
    ])
    expect(items[0]).toMatchObject({ role: 'assistant', text: 'part one, part two' })
  })

  it('omits meta when the turn has no provider attribution', () => {
    const items = fromTranscript([
      { seq: 1, kind: 'assistant', blocks: [{ type: 'text', text: 'x' }], created_at: at },
    ])
    expect(items[0]).toMatchObject({ role: 'assistant', meta: undefined })
  })

  it('groups tool items into the following assistant turn', () => {
    const items = fromTranscript([
      { seq: 1, kind: 'user', text: 'q', created_at: at },
      {
        seq: 2,
        kind: 'tool',
        tool: { call_id: 'c1', name: 'shell', status: 'ok', result_digest: 'notes.md', duration_ms: 42 },
        created_at: at,
      },
      { seq: 3, kind: 'assistant', blocks: [{ type: 'text', text: 'a' }], created_at: at },
    ])
    expect(items.map((i) => i.role)).toEqual(['user', 'assistant'])
    expect(items[1]).toMatchObject({
      role: 'assistant',
      text: 'a',
      tools: [{ id: 'c1', name: 'shell', status: 'ok', digest: 'notes.md', durationMs: 42 }],
    })
  })

  it('groups a run of several tool calls onto one following assistant turn', () => {
    const items = fromTranscript([
      {
        seq: 1,
        kind: 'tool',
        tool: { call_id: 'c1', name: 'web_search', status: 'ok', duration_ms: 10 },
        created_at: at,
      },
      {
        seq: 2,
        kind: 'tool',
        tool: { call_id: 'c2', name: 'web_fetch', status: 'ok', duration_ms: 20 },
        created_at: at,
      },
      { seq: 3, kind: 'assistant', blocks: [{ type: 'text', text: 'done' }], created_at: at },
    ])
    expect(items).toHaveLength(1)
    expect(items[0]).toMatchObject({ role: 'assistant', text: 'done' })
    if (items[0].role === 'assistant') {
      expect(items[0].tools.map((t) => t.id)).toEqual(['c1', 'c2'])
    }
  })

  it('flushes trailing tool calls with no following assistant turn as a bare activity item', () => {
    const items = fromTranscript([
      { seq: 1, kind: 'user', text: 'q', created_at: at },
      {
        seq: 2,
        kind: 'tool',
        tool: { call_id: 'c1', name: 'shell', status: 'error', duration_ms: 5 },
        created_at: at,
      },
    ])
    expect(items.map((i) => i.role)).toEqual(['user', 'assistant'])
    expect(items[1]).toMatchObject({
      id: 'replay-2',
      role: 'assistant',
      text: '',
      tools: [{ id: 'c1', status: 'error' }],
    })
  })

  it('flushes pending tools before a compaction or interrupted item, never leaking across them', () => {
    const items = fromTranscript([
      {
        seq: 1,
        kind: 'tool',
        tool: { call_id: 'c1', name: 'shell', status: 'ok' },
        created_at: at,
      },
      { seq: 2, kind: 'compaction', text: 'summarized', created_at: at },
      {
        seq: 3,
        kind: 'tool',
        tool: { call_id: 'c2', name: 'shell', status: 'ok' },
        created_at: at,
      },
      { seq: 4, kind: 'interrupted', text: 'partial', created_at: at },
    ])
    expect(items.map((i) => i.role)).toEqual(['assistant', 'compaction', 'assistant', 'interrupted'])
    expect(items[0]).toMatchObject({ tools: [{ id: 'c1' }] })
    expect(items[2]).toMatchObject({ tools: [{ id: 'c2' }] })
  })

  it('falls back to ok status for an unrecognized tool status', () => {
    const items = fromTranscript([
      {
        seq: 1,
        kind: 'tool',
        tool: { call_id: 'c1', name: 'shell', status: 'unknown' },
        created_at: at,
      },
    ])
    if (items[0].role === 'assistant') {
      expect(items[0].tools[0].status).toBe('ok')
    }
  })

  it('drops tool items with no tool payload', () => {
    const items = fromTranscript([
      { seq: 1, kind: 'user', text: 'q', created_at: at },
      { seq: 2, kind: 'tool', created_at: at },
      { seq: 3, kind: 'assistant', blocks: [{ type: 'text', text: 'a' }], created_at: at },
    ])
    expect(items.map((i) => i.role)).toEqual(['user', 'assistant'])
  })

  it('handles the empty transcript of a fresh session', () => {
    expect(fromTranscript([])).toEqual([])
  })
})
