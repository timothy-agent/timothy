import type { ReactNode } from 'react'
import type { MissionEvent } from '../../api/types'

// eventRenderers maps a mission_events kind to a short, human-readable
// summary of its payload. Unrecognized kinds fall back to the generic
// renderer below — forward-compatible with new event kinds the web
// hasn't been updated to render specifically, rather than rendering
// blank.
const renderers: Record<string, (payload: unknown) => ReactNode> = {
  'mission.created': () => 'Mission created',
  'mission.provisioned': (p) => {
    const { branch } = asRecord(p)
    return branch ? `Workspace provisioned on branch ${String(branch)}` : 'Workspace provisioned'
  },
  'mission.phase_started': (p) => {
    const { phase } = asRecord(p)
    return `Phase started: ${String(phase ?? '?')}`
  },
  'mission.plan_created': (p) => {
    const { units } = asRecord(p)
    return `Plan created with ${String(units ?? '?')} unit(s)`
  },
  'mission.worker_started': () => 'Worker turn started',
  'mission.worker_finished': () => 'Worker turn finished',
  'mission.progress': (p) => {
    const { note } = asRecord(p)
    return String(note ?? '')
  },
  'mission.unit_verified': (p) => {
    const { unit, passed } = asRecord(p)
    const label = unit != null ? `Unit ${String(unit)} verification` : 'Unit verification'
    return `${label}: ${passed ? 'passed' : 'failed'}`
  },
  'mission.review_verdict': (p) => {
    const { decision } = asRecord(p)
    return `Review verdict: ${String(decision ?? '?')}`
  },
  'mission.retry': () => 'Retrying',
  'mission.blocked': (p) => {
    const { question } = asRecord(p)
    return `Blocked: ${String(question ?? 'waiting on the user')}`
  },
  'mission.permission_requested': (p) => {
    const { tool, args, danger } = asRecord(p)
    const dangerSuffix = danger && danger !== 'safe' ? ` (${String(danger)})` : ''
    return (
      <span>
        Permission requested: {String(tool ?? 'a tool call')}
        {dangerSuffix}
        {args ? (
          <code className="ml-1 rounded bg-muted px-1 py-0.5 text-xs whitespace-pre-wrap">
            {truncateForDisplay(String(args))}
          </code>
        ) : null}
      </span>
    )
  },
  'mission.permission_answered': (p) => {
    const { tool, decision } = asRecord(p)
    return `Permission ${String(decision ?? '?')}: ${String(tool ?? 'a tool call')}`
  },
  'mission.paused': (p) => {
    const { reason, detail } = asRecord(p)
    const base = `Paused (${String(reason ?? 'unknown reason')})`
    return detail ? `${base}: ${String(detail)}` : base
  },
  'mission.resumed': () => 'Resumed',
  'mission.recovery': () => 'Recovered after a restart',
  'mission.violation': () => 'Policy violation detected',
  'mission.done': () => 'Mission completed',
  'mission.failed': (p) => {
    const { reason } = asRecord(p)
    return `Mission failed${reason ? `: ${String(reason)}` : ''}`
  },
  'mission.reconciled': (p) => {
    const { canonical_phase } = asRecord(p)
    return `Conflicting outcome reconciled (canonical: ${String(canonical_phase ?? '?')})`
  },
  'mission.pushed': (p) => {
    const { branch, remote_host } = asRecord(p)
    return `Pushed ${String(branch ?? '?')} to ${String(remote_host ?? '?')}`
  },
  'mission.push_failed': (p) => {
    const { reason } = asRecord(p)
    return `Push failed: ${String(reason ?? 'unknown reason')}`
  },
}

function asRecord(v: unknown): Record<string, unknown> {
  return typeof v === 'object' && v !== null ? (v as Record<string, unknown>) : {}
}

// truncateForDisplay caps an inline Timeline snippet well below the
// backend's own 2000-char event cap — a full shell command belongs in
// the (already truncated) event payload, not a single Timeline row.
function truncateForDisplay(s: string, n = 200): string {
  return s.length <= n ? s : s.slice(0, n) + '…'
}

export function renderEvent(event: MissionEvent): ReactNode {
  const render = renderers[event.kind]
  if (render) return render(event.payload)
  return event.kind
}
