import { describe, expect, it } from 'vitest'
import { cronPresets, describeCron, describeTrigger, isCronShape, presetFor } from './cron'

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

describe('isCronShape', () => {
  it.each(['0 7 * * *', '*/15 9-17 * * 1-5', '0 8 1,15 * MON', '@daily', '@every 1h', '  0 7 * * *  '])(
    'accepts %s',
    (expr) => {
      expect(isCronShape(expr)).toBe(true)
    },
  )

  it.each(['', '0 7 * *', '0 7 * * * *', 'every morning', '@often', '0 7 * * $'])('rejects %s', (expr) => {
    expect(isCronShape(expr)).toBe(false)
  })
})

describe('describeTrigger', () => {
  it('describes a cron trigger by its expression', () => {
    expect(describeTrigger({ kind: 'cron', config: { expr: '0 * * * *' } })).toBe('Hourly')
    expect(describeTrigger({ kind: 'cron', config: { expr: '*/5 * * * *' } })).toBe('*/5 * * * *')
  })

  it('names other kinds in plain words', () => {
    expect(describeTrigger({ kind: 'manual', config: {} })).toBe('manual')
    expect(describeTrigger({ kind: 'connector_event', config: {} })).toBe('connector event')
  })
})
