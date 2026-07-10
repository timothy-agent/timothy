import { describe, expect, it } from 'vitest'
import type { SessionMeta } from '../api/types'
import { groupByDay } from './sessions'

const session = (id: string, updatedAt: string): SessionMeta => ({
  id,
  title: id,
  archived: false,
  created_at: updatedAt,
  updated_at: updatedAt,
})

describe('groupByDay', () => {
  it('buckets into Today, Yesterday, and dated groups', () => {
    const now = new Date('2026-07-10T15:00:00')
    const groups = groupByDay(
      [
        session('a', '2026-07-10T14:00:00'),
        session('b', '2026-07-10T09:00:00'),
        session('c', '2026-07-09T22:00:00'),
        session('d', '2026-07-01T08:00:00'),
      ],
      now,
    )

    expect(groups.map((g) => g.label)).toEqual(['Today', 'Yesterday', 'July 1'])
    expect(groups[0].sessions.map((s) => s.id)).toEqual(['a', 'b'])
    expect(groups[1].sessions.map((s) => s.id)).toEqual(['c'])
    expect(groups[2].sessions.map((s) => s.id)).toEqual(['d'])
  })

  it('returns nothing for an empty list', () => {
    expect(groupByDay([], new Date('2026-07-10T15:00:00'))).toEqual([])
  })
})
