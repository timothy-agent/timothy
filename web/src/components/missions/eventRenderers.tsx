import type { ReactNode } from 'react'
import type {
  ExecutorAuthFailedPayload,
  ExecutorDiedPayload,
  ExecutorIdleKilledPayload,
  ExecutorResultPayload,
  ExecutorSpawnedPayload,
  MissionEvent,
} from '../../api/types'
import { formatDuration } from '../../lib/format'

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
    const label = typeof unit === 'number' ? `Unit ${unit + 1} verification` : 'Unit verification'
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
  'executor.spawned': (p) => {
    const { harness, provider, model, auth_mode } = p as ExecutorSpawnedPayload
    return (
      <span>
        Delegated to {harness} ·{' '}
        <span className="font-mono">
          {provider}/{model}
        </span>{' '}
        · {auth_mode}
        <span className="ml-1.5 rounded bg-brand-soft px-1.5 py-0.5 text-[10px] font-semibold text-brand-soft-foreground">
          executor
        </span>
      </span>
    )
  },
  'executor.died': (p) => {
    const { reason, exit_code } = p as ExecutorDiedPayload
    return (
      <span className="text-red-400">
        Executor died: {reason}
        {exit_code !== undefined ? ` (exit ${exit_code})` : ''}
      </span>
    )
  },
  'executor.idle_killed': (p) => {
    const { idle_s } = p as ExecutorIdleKilledPayload
    return <span className="text-red-400">Executor killed: idle for {idle_s}s</span>
  },
  'executor.auth_failed': (p) => {
    const { harness } = p as ExecutorAuthFailedPayload
    return <span className="text-red-400">{harness} auth failed — re-run the executor login</span>
  },
}

// formatExecutorCost renders executor.result's usage.cost_usd:
// non-null is the billed truth ($x.xxxx, never guessed — D-013);
// null with a preceding subscription or oauth_token spawn means cost is
// genuinely untracked (both ride the user's existing subscription, no
// per-call price), distinct from a null we simply failed to attribute.
function formatExecutorCost(costUsd: number | null | undefined, subscriptionAuth?: boolean): string {
  if (typeof costUsd === 'number') return `$${costUsd.toFixed(4)}`
  return subscriptionAuth ? 'subscription — cost untracked' : 'cost unreported'
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

// renderEvent looks up the fixed per-kind renderer above; unknown
// kinds fall back to the raw kind string rather than rendering blank.
// allEvents carries the full timeline so executor.result (below) can
// look back to the run's executor.spawned for context its own payload
// doesn't repeat (auth_mode) — every other kind ignores it.
export function renderEvent(event: MissionEvent, allEvents: MissionEvent[] = [event]): ReactNode {
  if (event.kind === 'executor.result') return renderExecutorResult(event, allEvents)
  const render = renderers[event.kind]
  if (render) return render(event.payload)
  return event.kind
}

function renderExecutorResult(event: MissionEvent, allEvents: MissionEvent[]): ReactNode {
  const { status, duration_ms, exit_code, denials, usage } = event.payload as ExecutorResultPayload
  const spawn = [...allEvents]
    .slice(0, allEvents.indexOf(event))
    .reverse()
    .find((e) => e.kind === 'executor.spawned') as MissionEvent | undefined
  const spawnAuthMode = (spawn?.payload as ExecutorSpawnedPayload | undefined)?.auth_mode
  const subscriptionAuth = spawnAuthMode === 'subscription' || spawnAuthMode === 'oauth_token'
  return (
    <span>
      Executor finished: {status} · {formatDuration(duration_ms)} · exit {exit_code} ·{' '}
      {usage.input_tokens}→{usage.output_tokens} tok · {formatExecutorCost(usage.cost_usd, subscriptionAuth)}
      {denials.length > 0 && <span className="text-amber-400"> · denials: {denials.join(', ')}</span>}
    </span>
  )
}
