import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { isoToLocalInput, localInputToIso } from './datetimeLocal'

describe('datetimeLocal', () => {
  beforeEach(() => {
    vi.stubEnv('TZ', 'Europe/Amsterdam')
  })
  afterEach(() => {
    vi.unstubAllEnvs()
  })

  it('renders a stored UTC value in local time', () => {
    expect(isoToLocalInput('2026-08-01T12:30:00Z')).toBe('2026-08-01T14:30')
  })

  it('round-trips UTC to local to UTC without drift', () => {
    const stored = '2026-08-01T12:30:00Z'
    expect(localInputToIso(isoToLocalInput(stored))).toBe('2026-08-01T12:30:00.000Z')
  })

  it('does not treat the UTC wall clock as local time (the 16-char slice bug)', () => {
    const stored = '2026-12-01T06:00:00Z'
    expect(isoToLocalInput(stored)).not.toBe(stored.slice(0, 16))
    expect(localInputToIso(isoToLocalInput(stored))).toBe('2026-12-01T06:00:00.000Z')
  })

  it('returns an empty string for a missing or invalid value', () => {
    expect(isoToLocalInput(undefined)).toBe('')
    expect(isoToLocalInput('')).toBe('')
    expect(isoToLocalInput('not a date')).toBe('')
  })
})
