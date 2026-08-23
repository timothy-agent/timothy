import { Link } from 'react-router'
import type { Mission } from '../../api/types'
import { missionDisplayName, relativeTime } from '../../lib/format'
import { BrandMark } from '../BrandMark'
import { ClaudeCodeIcon } from '../icons/ClaudeCodeIcon'
import { OpenAIIcon } from '../icons/OpenAIIcon'
import { OpenCodeIcon } from '../icons/OpenCodeIcon'
import { PiIcon } from '../icons/PiIcon'

const statusColor: Record<Mission['status'], string> = {
  idle: 'bg-muted text-muted-foreground',
  working: 'bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-300',
  waiting_for_input: 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300',
  paused: 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300',
  done: 'bg-green-100 text-green-800 dark:bg-green-950 dark:text-green-300',
  error: 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300',
}

// harnessDisplayNames mirrors MissionDetail's own map (D-051 harness
// ids to labels) — kept separate since that one lives with its own
// event-derived HarnessIcon, not the mission row's own harness field.
const harnessDisplayNames: Record<string, string> = {
  'claude-cli': 'Claude Code',
  pi: 'pi',
  'codex-cli': 'Codex CLI',
  opencode: 'OpenCode',
}

function harnessLabel(harness?: string): string {
  if (!harness) return 'Native'
  return harnessDisplayNames[harness] ?? harness
}

function HarnessIcon({ harness }: { harness?: string }) {
  if (harness === 'pi') return <PiIcon />
  if (harness === 'codex-cli') return <OpenAIIcon />
  if (harness === 'opencode') return <OpenCodeIcon />
  if (harness === 'claude-cli') return <ClaudeCodeIcon />
  return <BrandMark className="size-3.5 shrink-0 rounded-[3px]" />
}

// statusLabel renders the pill text: non-terminal statuses print as-is
// (status.replace), but a terminal mission never shows the raw status
// "error" — phase=failed reads as "cancelled" (failure_reason) or
// "failed", phase=done reads "done".
function statusLabel(mission: Mission): string {
  if (mission.phase === 'done') return 'done'
  if (mission.phase === 'failed') return mission.failure_reason === 'cancelled' ? 'cancelled' : 'failed'
  return mission.status.replace(/_/g, ' ')
}

function statusPillColor(mission: Mission): string {
  if (mission.phase === 'done') return statusColor.done
  if (mission.phase === 'failed') {
    return mission.failure_reason === 'cancelled' ? statusColor.idle : statusColor.error
  }
  return statusColor[mission.status]
}

export function MissionCard({ mission }: { mission: Mission }) {
  return (
    <Link
      to={`/missions/${mission.id}`}
      className="flex flex-col gap-2 rounded-xl border border-border bg-card p-4 text-left shadow-sm transition hover:border-brand hover:shadow-md"
    >
      <div className="flex items-start justify-between gap-2">
        <span className="line-clamp-2 text-sm font-semibold">{missionDisplayName(mission)}</span>
        <span
          className={`shrink-0 rounded px-1.5 py-0.5 text-xs font-medium whitespace-nowrap ${statusPillColor(mission)}`}
        >
          {statusLabel(mission)}
        </span>
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span className="capitalize">{mission.kind}</span>
        {mission.schedule_id && (
          <span className="rounded bg-brand-soft px-1.5 py-0.5 text-xs font-semibold text-brand-soft-foreground">
            recurring
          </span>
        )}
        <span className="inline-flex items-center gap-1">
          <HarnessIcon harness={mission.harness} />
          {harnessLabel(mission.harness)}
        </span>
        {mission.top_model && <span className="max-w-32 truncate">{mission.top_model}</span>}
        <span title={new Date(mission.created_at).toLocaleString()}>
          {relativeTime(mission.created_at)}
        </span>
      </div>
      {mission.pause_message && (
        <p className="line-clamp-2 text-xs text-amber-700 dark:text-amber-400">{mission.pause_message}</p>
      )}
    </Link>
  )
}
