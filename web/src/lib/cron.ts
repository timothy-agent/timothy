import type { AutomationTrigger, AutomationTriggerConfig } from '../api/types'

// cronPresets covers the common recurring shapes the automation form
// offers directly; 'custom' has no cron value of its own: it means
// "let the user type one," matched last by presetFor.
export const cronPresets = [
  { value: 'daily-7am', label: 'Daily, 7:00 AM', cron: '0 7 * * *' },
  { value: 'weekdays-8am', label: 'Weekdays, 8:00 AM', cron: '0 8 * * 1-5' },
  { value: 'hourly', label: 'Hourly', cron: '0 * * * *' },
  { value: 'custom', label: 'Custom', cron: null },
] as const

export type CronPresetValue = (typeof cronPresets)[number]['value']

// presetFor maps a stored cron expression back to the preset that
// produced it, so editing an automation reopens the same preset it was
// created with rather than always falling back to "Custom".
export function presetFor(cron: string): CronPresetValue {
  const match = cronPresets.find((p) => p.cron === cron)
  return match?.value ?? 'custom'
}

// describeCron renders a cron expression as plain English for the
// handful of shapes this UI ever produces; anything else is shown
// verbatim (still valid cron, just not one of our presets).
export function describeCron(cron: string): string {
  const preset = cronPresets.find((p) => p.cron === cron)
  if (preset) return preset.label
  return cron
}

// Five fields of cron characters, or a robfig descriptor such as @daily.
const cronShape =
  /^(@(yearly|annually|monthly|weekly|daily|midnight|hourly)|@every\s+\S+|[0-9A-Za-z*?/,-]+(\s+[0-9A-Za-z*?/,-]+){4})$/

// isCronShape is a permissive client check; the server parser has the final say.
export function isCronShape(expr: string): boolean {
  return cronShape.test(expr.trim())
}

// describeTrigger labels a trigger for a badge: its cron, its repo and
// events, its webhook scheme, or its kind.
export function describeTrigger(t: { kind: AutomationTrigger['kind']; config: AutomationTriggerConfig }): string {
  if (t.kind === 'cron') return t.config.expr ? describeCron(t.config.expr) : 'cron'
  if (t.kind === 'connector_event' && t.config.repo) {
    const events = t.config.events ?? []
    return events.length > 0 ? `GitHub ${t.config.repo}: ${events.join(', ')}` : `GitHub ${t.config.repo}`
  }
  if (t.kind === 'webhook' && t.config.scheme) return `Webhook (${t.config.scheme})`
  return t.kind.replace('_', ' ')
}

// hookPath is a webhook trigger's endpoint, relative to the brain; the
// operator fronts it with a tunnel.
export function hookPath(triggerId: string): string {
  return `/hooks/${triggerId}`
}
