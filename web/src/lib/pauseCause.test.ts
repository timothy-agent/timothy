import { describe, expect, it } from 'vitest'
import { pauseCauseLabel, pauseDetailText } from './pauseCause'

describe('pauseCauseLabel', () => {
  const cases: [string, Parameters<typeof pauseCauseLabel>[0], string][] = [
    ['mixed_currency', { cause: 'mixed_currency' }, 'spend in an unconvertible currency'],
    ['budget', { cause: 'budget' }, 'budget exhausted'],
    ['review_infra', { cause: 'review_infra' }, 'review could not run'],
    ['review_budget', { cause: 'review_budget' }, 'review token budget exhausted'],
    ['consecutive_failures', { cause: 'consecutive_failures' }, 'repeated worker failures'],
    ['stalled_retries', { cause: 'stalled_retries' }, 'same gap on repeated retries'],
    ['findings_untouched', { cause: 'findings_untouched' }, 'worker left the named files untouched'],
    ['review_rounds_exhausted', { cause: 'review_rounds_exhausted' }, 'review rounds exhausted, findings still open'],
    ['result_failed', { cause: 'result_failed' }, 'delivery failed in the result phase'],
    ['plan_approval', { cause: 'plan_approval' }, 'plan awaiting approval'],
    ['model_floor', { cause: 'model_floor', model: 'qwen2.5:7b' }, 'qwen2.5:7b is below the mission floor'],
    ['model_floor without a model', { cause: 'model_floor' }, 'model is below the mission floor'],
    ['harness retries in plan', { cause: 'harness_retries_exhausted', phase: 'plan', harness_retries: 3 }, 'plan rejected 3 times'],
    ['harness retries in plan, once', { cause: 'harness_retries_exhausted', phase: 'plan', harness_retries: 1 }, 'plan rejected 1 time'],
    ['harness retries in build', { cause: 'harness_retries_exhausted', phase: 'build', harness_retries: 5 }, 'harness failed 5 times'],
  ]
  it.each(cases)('%s', (_name, payload, want) => {
    expect(pauseCauseLabel(payload)).toBe(want)
  })

  it('returns undefined without a known cause', () => {
    expect(pauseCauseLabel(undefined)).toBeUndefined()
    expect(pauseCauseLabel({})).toBeUndefined()
    expect(pauseCauseLabel({ cause: 'future_cause' })).toBeUndefined()
  })
})

describe('pauseDetailText', () => {
  it('prefixes the last rejection for a plan harness pause', () => {
    expect(
      pauseDetailText({ cause: 'harness_retries_exhausted', phase: 'plan', detail: 'plan_invalid: no criteria' }),
    ).toBe('Last rejection: plan_invalid: no criteria')
  })

  it('passes other details through', () => {
    expect(pauseDetailText({ cause: 'budget', detail: 'over' })).toBe('over')
    expect(pauseDetailText({ cause: 'budget' })).toBeUndefined()
  })

  it('replaces a model floor error with what to do', () => {
    expect(
      pauseDetailText({ cause: 'model_floor', model: 'qwen2.5:7b', detail: 'mission turn served by a below-floor model: qwen2.5:7b' }),
    ).toBe('This model can chat but cannot run missions. Pick a stronger model for missions in Settings, then resume.')
  })
})
