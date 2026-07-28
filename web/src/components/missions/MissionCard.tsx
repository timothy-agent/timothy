import { Link } from 'react-router'
import type { Mission } from '../../api/types'

const statusColor: Record<Mission['status'], string> = {
  idle: 'bg-muted text-muted-foreground',
  working: 'bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-300',
  waiting_for_input: 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300',
  paused: 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300',
  done: 'bg-green-100 text-green-800 dark:bg-green-950 dark:text-green-300',
  error: 'bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300',
}

function unitProgress(mission: Mission): string | null {
  const units = mission.spec?.units
  if (!units || units.length === 0) return null
  const done = units.filter((u) => u.passes).length
  return `${done}/${units.length} units`
}

export function MissionCard({ mission }: { mission: Mission }) {
  const progress = unitProgress(mission)
  return (
    <Link
      to={`/missions/${mission.id}`}
      className="flex flex-col gap-2 rounded-xl border border-border bg-card p-4 text-left shadow-sm transition hover:border-brand hover:shadow-md"
    >
      <div className="flex items-start justify-between gap-2">
        <span className="line-clamp-2 text-sm font-semibold">{mission.goal}</span>
        <span
          className={`shrink-0 rounded px-1.5 py-0.5 text-xs font-medium whitespace-nowrap ${statusColor[mission.status]}`}
        >
          {mission.status.replace(/_/g, ' ')}
        </span>
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <span className="capitalize">{mission.kind}</span>
        <span>{mission.phase}</span>
        {mission.schedule_id && (
          <span className="rounded bg-brand-soft px-1.5 py-0.5 text-xs font-semibold text-brand-soft-foreground">
            recurring
          </span>
        )}
        <span>
          iteration {mission.iteration}/{mission.max_iterations}
        </span>
        {progress && <span>{progress}</span>}
      </div>
      {mission.pause_message && (
        <p className="line-clamp-2 text-xs text-amber-700 dark:text-amber-400">{mission.pause_message}</p>
      )}
    </Link>
  )
}
