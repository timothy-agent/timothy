// Pause causes (D-136, issue #1013): mirrors the Cause* constants in
// internal/brain/missions/statemachine.go. The mission.paused payload's
// `cause` is finer than the mission's pause_reason, so the banner names it.

export interface PausePayload {
  cause?: string
  phase?: string
  harness_retries?: number
  detail?: string
  model?: string
}

const causeLabels: Record<string, string> = {
  mixed_currency: 'spend in an unconvertible currency',
  budget: 'budget exhausted',
  review_infra: 'review could not run',
  review_budget: 'review token budget exhausted',
  consecutive_failures: 'repeated worker failures',
  stalled_retries: 'same gap on repeated retries',
  findings_untouched: 'worker left the named files untouched',
  review_rounds_exhausted: 'review rounds exhausted, findings still open',
  result_failed: 'delivery failed in the result phase',
  plan_approval: 'plan awaiting approval',
  model_floor: 'model is below the mission floor',
}

// pauseCauseLabel returns the banner label for a pause payload, or
// undefined when the payload has no known cause (older events).
export function pauseCauseLabel(p: PausePayload | undefined): string | undefined {
  if (!p?.cause) return undefined
  if (p.cause === 'harness_retries_exhausted') {
    const n = p.harness_retries
    const times = typeof n === 'number' ? `${n} ${n === 1 ? 'time' : 'times'}` : 'repeatedly'
    return p.phase === 'plan' ? `plan rejected ${times}` : `harness failed ${times}`
  }
  if (p.cause === 'model_floor' && p.model) return `${p.model} is below the mission floor`
  return causeLabels[p.cause]
}

// pauseDetailText prefixes the last rejection reason for a plan pause
// and replaces a model floor error with what to do about it.
export function pauseDetailText(p: PausePayload | undefined): string | undefined {
  if (p?.cause === 'model_floor') {
    return 'This model can chat but cannot run missions. Pick a stronger model for missions in Settings, then resume.'
  }
  if (!p?.detail) return undefined
  const planRejected = p.cause === 'harness_retries_exhausted' && p.phase === 'plan'
  return planRejected ? `Last rejection: ${p.detail}` : p.detail
}
