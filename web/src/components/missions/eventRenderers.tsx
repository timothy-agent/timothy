import type { ReactNode } from 'react'
import type {
  ExecutorAuthFailedPayload,
  ExecutorDiedPayload,
  ExecutorIdleKilledPayload,
  ExecutorResultPayload,
  ExecutorSkippedPayload,
  ExecutorSpawnedPayload,
  MissionEvent,
  MissionPermissionDeniedPayload,
  MissionPROpenedPayload,
  MissionRetryPayload,
  MissionSteeredPayload,
  MissionToolCallPayload,
  MissionTurnPayload,
} from '../../api/types'
import { formatDuration } from '../../lib/format'

// eventRenderers maps a mission_events kind to a short, human-readable
// summary of its payload. Unrecognized kinds fall back to the generic
// renderer below: forward-compatible with new event kinds the web
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
    return (
      <span className={passed ? 'text-green-400' : 'text-red-400'}>
        {label}: {passed ? 'passed' : 'failed'}
      </span>
    )
  },
  'mission.review_verdict': (p) => {
    const { decision } = asRecord(p)
    const approved = decision === 'approve'
    return <span className={approved ? 'text-green-400' : 'text-amber-400'}>Review verdict: {String(decision ?? '?')}</span>
  },
  'mission.turn': (p) => {
    const { phase, duration_ms, ok, reason } = p as MissionTurnPayload
    const base = `Turn (${phase}): ${ok ? 'ok' : 'failed'} · ${formatDuration(duration_ms)}`
    if (ok || !reason) return <span className={ok ? undefined : 'text-red-400'}>{base}</span>
    return (
      <span className="text-red-400" title={reason}>
        {base}: {truncateForDisplay(reason, 160)}
      </span>
    )
  },
  'mission.tool_call': (p) => {
    const { tool, status, duration_ms } = p as MissionToolCallPayload
    return (
      <span className={toolCallStatusClass(status)}>
        {tool} · {status} · {formatDuration(duration_ms)}
      </span>
    )
  },
  'mission.retry': (p) => {
    const { cause, reason } = p as MissionRetryPayload
    const label = cause ? `Retrying (${cause})` : 'Retrying'
    if (!reason) return <span className="text-amber-400">{label}</span>
    return (
      <span className="text-amber-400" title={reason}>
        {label}: {truncateForDisplay(reason, 160)}
      </span>
    )
  },
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
  'mission.permission_denied': (p) => {
    const { tool, detail } = p as MissionPermissionDeniedPayload
    return (
      <span className="text-red-400" title={detail}>
        Permission denied: {tool}
        {detail ? `: ${truncateForDisplay(detail, 160)}` : ''}
      </span>
    )
  },
  'mission.paused': (p) => {
    const { reason, detail } = asRecord(p)
    const base = `Paused (${String(reason ?? 'unknown reason')})`
    return <span className="text-amber-400">{detail ? `${base}: ${String(detail)}` : base}</span>
  },
  'mission.resumed': () => 'Resumed',
  'mission.steered': (p) => {
    const { note } = p as MissionSteeredPayload
    return <span className="text-amber-400">Operator note: {note}</span>
  },
  'mission.recovery': () => 'Recovered after a restart',
  'mission.violation': () => <span className="text-red-400">Policy violation detected</span>,
  'mission.done': () => <span className="text-green-400">Mission completed</span>,
  'mission.failed': (p) => {
    const { reason } = asRecord(p)
    return <span className="text-red-400">Mission failed{reason ? `: ${String(reason)}` : ''}</span>
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
    return <span className="text-red-400">Push failed: {String(reason ?? 'unknown reason')}</span>
  },
  'mission.pr_opened': (p) => {
    const { url, number } = p as MissionPROpenedPayload
    return (
      <span>
        Pull request opened:{' '}
        <a href={url} target="_blank" rel="noreferrer" className="underline underline-offset-2 hover:text-foreground">
          #{number}
        </a>
      </span>
    )
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
          harness
        </span>
      </span>
    )
  },
  'executor.died': (p) => {
    const { reason, exit_code } = p as ExecutorDiedPayload
    return (
      <span className="text-red-400">
        Harness died: {reason}
        {exit_code !== undefined ? ` (exit ${exit_code})` : ''}
      </span>
    )
  },
  'executor.idle_killed': (p) => {
    const { idle_s } = p as ExecutorIdleKilledPayload
    return <span className="text-red-400">Harness killed: idle for {idle_s}s</span>
  },
  'executor.auth_failed': (p) => {
    const { harness } = p as ExecutorAuthFailedPayload
    return <span className="text-red-400">{harness} auth failed — re-run the harness login</span>
  },
  'executor.skipped': (p) => {
    const { reason, error, until, provider, model, skip_reasons } = p as ExecutorSkippedPayload
    return (
      <span className="text-amber-400">
        Harness skipped: {reason}
        {reason === 'resolve_failed' && error ? ` — ${truncateForDisplay(error)}` : ''}
        {reason === 'cooldown' && until ? ` — ${provider}/${model} until ${until}` : ''}
        {reason === 'no_usable_entry' && skip_reasons && skip_reasons.length > 0
          ? ` — ${skip_reasons.join(', ')}`
          : ''}
      </span>
    )
  },
}

// formatExecutorCost renders executor.result's usage.cost_usd.
// cost_usd_billed=true means this figure is the SAME one the cost
// ledger booked as real spend (Anthropic first-party api_key): shown
// as the billed truth, unchanged from before. Everything else is the
// CLI's own harness-reported figure, never booked as-is: subscription/
// oauth_token auth (never billed at all) or a non-anthropic provider
// (priced against Anthropic's table, fiction for that provider: the
// ledger prices it from that provider's own rows instead, or leaves it
// unpriced): labeled so it's never mistaken for real marginal spend.
// Absent + subscription auth means cost is genuinely untracked (older
// run, before this CLI reported it); absent otherwise is unreported.
function formatExecutorCost(costUsd: number | null | undefined, billed: boolean, subscriptionAuth?: boolean): string {
  if (typeof costUsd === 'number') {
    const amount = `$${costUsd.toFixed(4)}`
    if (billed) return amount
    return subscriptionAuth ? `${amount} · subscription (not billed)` : `harness-reported ${amount}`
  }
  return subscriptionAuth ? 'subscription — cost untracked' : 'cost unreported'
}

// toolCallStatusClass colors a tool call trace entry by outcome:
// shared between the plain Timeline row above and TimelineSection's
// per-turn trace, so a tool call reads the same color wherever it's
// shown.
export function toolCallStatusClass(status: string): string {
  if (status === 'ok') return 'text-green-400'
  if (status === 'denied') return 'text-amber-400'
  return 'text-red-400'
}

function asRecord(v: unknown): Record<string, unknown> {
  return typeof v === 'object' && v !== null ? (v as Record<string, unknown>) : {}
}

// truncateForDisplay caps an inline Timeline snippet well below the
// backend's own 2000-char event cap: a full shell command belongs in
// the (already truncated) event payload, not a single Timeline row.
function truncateForDisplay(s: string, n = 200): string {
  return s.length <= n ? s : s.slice(0, n) + '…'
}

// renderEvent looks up the fixed per-kind renderer above; unknown
// kinds fall back to the raw kind string rather than rendering blank.
// allEvents carries the full timeline so executor.result (below) can
// look back to the run's executor.spawned for context its own payload
// doesn't repeat (auth_mode): every other kind ignores it.
export function renderEvent(event: MissionEvent, allEvents: MissionEvent[] = [event]): ReactNode {
  if (event.kind === 'executor.result') return renderExecutorResult(event, allEvents)
  const render = renderers[event.kind]
  if (render) return render(event.payload)
  return event.kind
}

function renderExecutorResult(event: MissionEvent, allEvents: MissionEvent[]): ReactNode {
  // denials/usage are omitted from the payload when empty: never
  // assume presence, a TypeError here blanks the whole detail page.
  const { status, duration_ms, exit_code, denials = [], usage } = event.payload as ExecutorResultPayload
  const spawn = [...allEvents]
    .slice(0, allEvents.indexOf(event))
    .reverse()
    .find((e) => e.kind === 'executor.spawned') as MissionEvent | undefined
  const spawnAuthMode = (spawn?.payload as ExecutorSpawnedPayload | undefined)?.auth_mode
  const subscriptionAuth = spawnAuthMode === 'subscription' || spawnAuthMode === 'oauth_token'
  return (
    <span>
      Harness finished: {status} · {formatDuration(duration_ms)} · exit {exit_code} ·{' '}
      {usage
        ? `${usage.input_tokens}→${usage.output_tokens} tok · ${formatExecutorCost(usage.cost_usd, !!usage.cost_usd_billed, subscriptionAuth)}`
        : 'no usage reported'}
      {denials.length > 0 && <span className="text-amber-400"> · denials: {denials.join(', ')}</span>}
    </span>
  )
}
