import { describe, expect, it } from 'vitest'
import { compact, formatDuration, missionDisplayName, money } from './format'

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
