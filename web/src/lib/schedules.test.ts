import { describe, expect, it } from 'vitest'
import { cronPresets, describeCron, presetFor } from './schedules'

describe('presetFor', () => {
  it('round-trips every non-custom preset cron back to its preset value', () => {
    for (const preset of cronPresets) {
      if (preset.cron === null) continue
      expect(presetFor(preset.cron)).toBe(preset.value)
    }
  })

  it('falls back to custom for an unrecognized cron', () => {
    expect(presetFor('*/5 * * * *')).toBe('custom')
  })
})

describe('describeCron', () => {
  it('describes a known preset in plain English', () => {
    expect(describeCron('0 7 * * *')).toBe('Daily, 7:00 AM')
  })

  it('shows an unrecognized cron verbatim', () => {
    expect(describeCron('*/5 * * * *')).toBe('*/5 * * * *')
  })
})
