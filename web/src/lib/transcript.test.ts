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

  it('skips tool items until the tool loop lands', () => {
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
