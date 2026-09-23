import { describe, expect, it } from 'vitest'
import type { Automation, AutomationTrigger } from '../api/types'
import { cronExpr, cronPresets, cronTrigger, describeCron, presetFor } from './cron'

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

function trigger(over: Partial<AutomationTrigger>): AutomationTrigger {
  return {
    id: 't1',
    automation_id: 'a1',
    kind: 'cron',
    config: { expr: '0 7 * * *' },
    state: {},
    enabled: true,
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
    ...over,
  }
}

function automation(triggers: AutomationTrigger[]): Automation {
  return {
    id: 'a1',
    name: 'daily-brief',
    description: '',
    agent_id: '00000000-0000-0000-0000-000000000001',
    action: { kind: 'mission', mission: { goal: 'brief', kind: 'general' } },
    concurrency: 'skip',
    max_concurrent: 1,
    max_runs_per_hour: 6,
    continuity: true,
    notes_enabled: true,
    consecutive_failures: 0,
    enabled: true,
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
    triggers,
    stats: { runs_total: 0, succeeded_7d: 0, failed_7d: 0 },
  }
}

describe('cronTrigger / cronExpr', () => {
  it('returns the first enabled cron trigger', () => {
    const a = automation([
      trigger({ id: 'm1', kind: 'manual', config: {} }),
      trigger({ id: 'c0', enabled: false, config: { expr: '0 * * * *' } }),
      trigger({ id: 'c1', config: { expr: '0 8 * * 1-5' } }),
    ])
    expect(cronTrigger(a)?.id).toBe('c1')
    expect(cronExpr(a)).toBe('0 8 * * 1-5')
  })

  it('returns undefined without an enabled cron trigger', () => {
    const a = automation([trigger({ id: 'm1', kind: 'manual', config: {} })])
    expect(cronTrigger(a)).toBeUndefined()
    expect(cronExpr(a)).toBeUndefined()
  })
})
