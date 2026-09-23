import type { AutomationTriggerInput } from '../../api/client'
import type { AutomationTrigger } from '../../api/types'
import { cronPresets, isCronShape } from '../../lib/cron'

// TriggerDraft is one editable trigger row; key is local, id is the
// server's and keeps the trigger's state on save.
export interface TriggerDraft {
  key: string
  id?: string
  kind: AutomationTrigger['kind']
  expr: string
  enabled: boolean
  toolAllowlist?: string[]
}

export const maxTriggers = 5
export const defaultCron = cronPresets[0].cron

let seq = 0
const nextKey = () => `trigger-${++seq}`

// newTriggerDraft is a fresh daily cron trigger.
export function newTriggerDraft(): TriggerDraft {
  return { key: nextKey(), kind: 'cron', expr: defaultCron, enabled: true }
}

// draftsFromTriggers seeds rows from stored or template triggers.
export function draftsFromTriggers(
  triggers: { id?: string; kind: AutomationTrigger['kind']; config: { expr?: string }; enabled?: boolean; tool_allowlist?: string[] }[],
): TriggerDraft[] {
  return triggers.map((t) => ({
    key: nextKey(),
    id: t.id,
    kind: t.kind,
    expr: t.config.expr ?? '',
    enabled: t.enabled ?? true,
    toolAllowlist: t.tool_allowlist,
  }))
}

// draftsToInput builds the wire triggers; manual triggers carry no config.
export function draftsToInput(drafts: TriggerDraft[]): AutomationTriggerInput[] {
  return drafts.map((d) => ({
    ...(d.id && { id: d.id }),
    kind: d.kind,
    config: d.kind === 'cron' ? { expr: d.expr.trim() } : {},
    enabled: d.enabled,
    ...(d.toolAllowlist && { tool_allowlist: d.toolAllowlist }),
  }))
}

// cronError is the client-side message for a draft, or undefined.
export function cronError(d: TriggerDraft): string | undefined {
  if (d.kind !== 'cron' || isCronShape(d.expr)) return undefined
  return 'Use five fields: minute, hour, day of month, month, weekday.'
}
