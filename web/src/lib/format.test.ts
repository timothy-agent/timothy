import { describe, expect, it } from 'vitest'
import {
  compact,
  formatDuration,
  humanBytes,
  missionDisplayName,
  money,
  relativeTime,
  relativeTimeUntil,
} from './format'

describe('compact', () => {
  it('leaves small numbers as-is', () => {
    expect(compact(0)).toBe('0')
    expect(compact(999)).toBe('999')
  })

  it('formats thousands with a k suffix', () => {
    expect(compact(1_000)).toBe('1.0k')
    expect(compact(120_000)).toBe('120.0k')
  })

  it('formats millions with an M suffix', () => {
    expect(compact(1_000_000)).toBe('1.0M')
    expect(compact(2_500_000)).toBe('2.5M')
  })
})

describe('formatDuration', () => {
  it('renders sub-second durations in milliseconds', () => {
    expect(formatDuration(42)).toBe('42ms')
  })

  it('renders sub-minute durations in seconds with one decimal', () => {
    expect(formatDuration(1500)).toBe('1.5s')
    expect(formatDuration(6700)).toBe('6.7s')
  })

  it('renders durations of a minute or more as minutes plus whole seconds', () => {
    expect(formatDuration(81_000)).toBe('1m 21s')
  })
})

describe('missionDisplayName', () => {
  it('prefers name when set', () => {
    expect(missionDisplayName({ name: 'Fix Login Bug', goal: 'fix the login bug on staging' })).toBe(
      'Fix Login Bug',
    )
  })

  it('falls back to the goal, truncated, when name is empty', () => {
    const longGoal = 'a'.repeat(80)
    expect(missionDisplayName({ name: '', goal: longGoal })).toBe(`${'a'.repeat(60)}…`)
  })

  it('falls back to the goal unmodified when short enough', () => {
    expect(missionDisplayName({ name: '', goal: 'fix the bug' })).toBe('fix the bug')
  })

  it('falls back to the goal when name is undefined', () => {
    expect(missionDisplayName({ goal: 'fix the bug' })).toBe('fix the bug')
  })
})

describe('money', () => {
  it('renders a zero amount without decimals', () => {
    expect(money(0)).toBe('$0')
    expect(money(0, 'EUR')).toBe('€0')
  })

  it('renders amounts under 1 with 4 decimal places', () => {
    expect(money(0.5)).toBe('$0.5000')
  })

  it('renders amounts of 1 or more with 2 decimal places', () => {
    expect(money(8)).toBe('$8.00')
    expect(money(6.88, 'EUR')).toBe('€6.88')
  })

  it('defaults to USD when no currency is given', () => {
    expect(money(1.5)).toBe('$1.50')
  })

  it('uses the narrow currency symbol, not the ISO code', () => {
    expect(money(32.1, 'BDT')).toBe('৳32.10')
  })

  it('falls back to "CODE amount" for a currency code Intl rejects, without throwing', () => {
    expect(money(1.5, 'NOTACODE')).toBe('NOTACODE 1.50')
  })
})

describe('humanBytes', () => {
  it('renders sub-1024 byte counts as bytes', () => {
    expect(humanBytes(512)).toBe('512 B')
  })

  it('renders kilobytes with one decimal', () => {
    expect(humanBytes(2048)).toBe('2.0 KB')
  })

  it('renders megabytes and gigabytes', () => {
    expect(humanBytes(5 * 1024 * 1024)).toBe('5.0 MB')
    expect(humanBytes(2 * 1024 * 1024 * 1024)).toBe('2.0 GB')
  })
})

describe('relativeTime', () => {
  it('renders under a minute as "just now"', () => {
    expect(relativeTime(new Date(Date.now() - 10_000).toISOString())).toBe('just now')
  })

  it('renders minutes and hours ago', () => {
    expect(relativeTime(new Date(Date.now() - 5 * 60_000).toISOString())).toBe('5m ago')
    expect(relativeTime(new Date(Date.now() - 3 * 3_600_000).toISOString())).toBe('3h ago')
  })

  it('renders days ago within a week', () => {
    expect(relativeTime(new Date(Date.now() - 2 * 86_400_000).toISOString())).toBe('2d ago')
  })

  it('falls back to a locale date past a week', () => {
    const old = new Date(Date.now() - 10 * 86_400_000)
    expect(relativeTime(old.toISOString())).toBe(old.toLocaleDateString())
  })
})

describe('relativeTimeUntil', () => {
  it('renders under a minute as "due now"', () => {
    expect(relativeTimeUntil(new Date(Date.now() + 10_000).toISOString())).toBe('due now')
  })

  it('renders minutes and hours from now', () => {
    expect(relativeTimeUntil(new Date(Date.now() + 5 * 60_000).toISOString())).toBe('in 5m')
    expect(relativeTimeUntil(new Date(Date.now() + 3 * 3_600_000).toISOString())).toBe('in 3h')
  })

  it('renders days from now within a week', () => {
    expect(relativeTimeUntil(new Date(Date.now() + 2 * 86_400_000).toISOString())).toBe('in 2d')
  })

  it('falls back to a locale date past a week', () => {
    const future = new Date(Date.now() + 10 * 86_400_000)
    expect(relativeTimeUntil(future.toISOString())).toBe(future.toLocaleDateString())
  })

  it('never returns "just now" for a future timestamp (the fixed bug)', () => {
    const iso = new Date(Date.now() + 6 * 3_600_000).toISOString()
    expect(relativeTimeUntil(iso)).not.toBe('just now')
    expect(relativeTimeUntil(iso)).toMatch(/^in [56]h$/)
  })
})
