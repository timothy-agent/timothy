import { describe, expect, it } from 'vitest'
import {
  initialWizardState,
  wizardReducer,
  wizardSteps,
  type WizardState,
  type WizardStep,
} from './wizardState'

function at(step: WizardStep, extra: Partial<WizardState> = {}): WizardState {
  return { ...initialWizardState(false), step, ...extra }
}

describe('initialWizardState', () => {
  it('starts at welcome without a chat model', () => {
    expect(initialWizardState(false).step).toBe('welcome')
  })

  it('starts at roles when a chat model already works', () => {
    expect(initialWizardState(true).step).toBe('roles')
  })
})

describe('wizardReducer', () => {
  it.each([
    ['welcome', 'source'],
    ['verify', 'roles'],
    ['roles', 'basics'],
    ['basics', 'hello'],
    ['hello', 'hello'],
  ] as const)('next from %s goes to %s', (from, to) => {
    expect(wizardReducer(at(from), { type: 'next' }).step).toBe(to)
  })

  it.each(['source', 'provider'] as const)('next from %s needs its own action', (from) => {
    expect(wizardReducer(at(from), { type: 'next' }).step).toBe(from)
  })

  it.each([
    ['welcome', 'welcome'],
    ['source', 'welcome'],
    ['provider', 'source'],
    ['verify', 'provider'],
    ['roles', 'source'],
    ['basics', 'roles'],
    ['hello', 'basics'],
  ] as const)('back from %s goes to %s', (from, to) => {
    expect(wizardReducer(at(from), { type: 'back' }).step).toBe(to)
  })

  it('pickSource stores the preset and opens the provider step', () => {
    const s = wizardReducer(at('source'), { type: 'pickSource', presetId: 'ollama' })
    expect(s.step).toBe('provider')
    expect(s.presetId).toBe('ollama')
  })

  it('providerCreated stores the provider and starts a fresh check', () => {
    const s = wizardReducer(at('provider', { verifyError: 'old' }), { type: 'providerCreated', id: 'p1', name: 'Ollama' })
    expect(s.step).toBe('verify')
    expect(s.provider).toEqual({ id: 'p1', name: 'Ollama' })
    expect(s.verified).toBeNull()
    expect(s.verifyError).toBeNull()
  })

  it('verified records the result and stays on verify', () => {
    const result = { ok: true, latency_ms: 42, model: 'qwen3:8b' }
    const s = wizardReducer(at('verify'), { type: 'verified', result })
    expect(s.step).toBe('verify')
    expect(s.verified).toEqual(result)
    expect(s.verifyError).toBeNull()
  })

  it('verifyFailed stays on verify with the detail', () => {
    const s = wizardReducer(at('verify'), { type: 'verifyFailed', detail: 'connection refused' })
    expect(s.step).toBe('verify')
    expect(s.verifyError).toBe('connection refused')
    expect(s.verified).toBeNull()
  })

  it.each(wizardSteps)('skip from %s marks the wizard skipped', (step) => {
    const s = wizardReducer(at(step), { type: 'skip' })
    expect(s.skipped).toBe(true)
    expect(s.step).toBe(step)
  })
})
