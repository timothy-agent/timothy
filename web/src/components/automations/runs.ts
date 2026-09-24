import type { AutomationRun } from '../../api/types'
import { formatDuration } from '../../lib/format'

// runStatusLabel is the pill text per run status.
export const runStatusLabel: Record<AutomationRun['status'], string> = {
  queued: 'Queued',
  starting: 'Starting',
  running: 'Running',
  done: 'Done',
  failed: 'Failed',
  skipped: 'Skipped',
}

// runDuration is finished minus started, or '' while the run is open.
export function runDuration(run: AutomationRun): string {
  if (!run.started_at || !run.finished_at) return ''
  return formatDuration(new Date(run.finished_at).getTime() - new Date(run.started_at).getTime())
}

// isPendingRun reports a run that has not reached a terminal status.
export function isPendingRun(run: AutomationRun): boolean {
  return run.status === 'queued' || run.status === 'starting' || run.status === 'running'
}

// runEventLabel is the trigger badge and muted summary for a run fired
// by a connector event ("owner/name #123") or a webhook (the delivery
// id, first 8 chars); undefined for cron, manual and run now.
export function runEventLabel(run: AutomationRun): { kind: string; summary: string } | undefined {
  const ev = (run.event ?? {}) as Record<string, unknown>
  const str = (v: unknown) => (typeof v === 'string' ? v : '')
  if (ev.source === 'connector' && str(ev.kind)) {
    const repo = str(ev.repo)
    const number = typeof ev.number === 'number' && ev.number > 0 ? ` #${ev.number}` : ''
    return { kind: str(ev.kind), summary: `${repo}${number}`.trim() }
  }
  if (ev.source === 'webhook' || ev.kind === 'webhook.received') {
    return { kind: 'webhook', summary: str(ev.delivery).slice(0, 8) }
  }
  return undefined
}
