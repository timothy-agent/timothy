import { Link } from 'react-router'
import type { Mission } from '../../api/types'
import { missionDisplayName, relativeTime } from '../../lib/format'
import { missionStatus } from '../timothy/status'
import { StatusBadge } from '../timothy/status-badge'
import { Badge } from '../ui/badge'
import { Card } from '../ui/card'
import { HarnessIcon, harnessLabel } from './HarnessIcon'
import { phaseStepText } from './PhaseStepper'

// statusLabel renders the pill text: non-terminal statuses print as-is
// (status.replace), but a terminal mission never shows the raw status
// "error" — phase=failed reads as "cancelled" (failure_reason) or
// "failed", phase=done reads "done".
function statusLabel(mission: Mission): string {
  if (mission.phase === 'done') return 'done'
  if (mission.phase === 'failed') return mission.failure_reason === 'cancelled' ? 'cancelled' : 'failed'
  return mission.status.replace(/_/g, ' ')
}

export function MissionCard({
  mission,
  cost,
  nextRun,
}: {
  mission: Mission
  cost?: string
  nextRun?: string
}) {
  return (
    <Card interactive asChild>
      <Link to={`/missions/${mission.id}`}>
        <div className="flex flex-wrap items-center gap-1.5">
          <StatusBadge status={missionStatus(mission)} label={statusLabel(mission)} size="sm" />
          {mission.schedule_id && (
            <Badge variant="secondary" size="sm">
              recurring
            </Badge>
          )}
          {mission.phase === 'plan' && mission.pause_reason === 'approval' && (
            <Badge variant="warning" size="sm">
              needs approval
            </Badge>
          )}
          {mission.pending_input && (
            <Badge variant="warning" size="sm">
              needs answer
            </Badge>
          )}
          <span className="ml-auto flex items-center gap-1 text-xs text-muted-foreground">
            <HarnessIcon harness={mission.harness} />
            {harnessLabel(mission.harness)}
          </span>
        </div>
        <p className="mt-3 line-clamp-2 text-sm font-medium">{missionDisplayName(mission)}</p>
        <div className="mt-2 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs text-muted-foreground">
          {/* The status badge above already says done/failed; the phase
              line only adds information while the mission is running. */}
          {mission.phase !== 'done' && mission.phase !== 'failed' && (
            <>
              <span className="whitespace-nowrap">{phaseStepText(mission)}</span>
              <span aria-hidden>&middot;</span>
            </>
          )}
          <span className="whitespace-nowrap capitalize">{mission.kind}</span>
          {mission.top_model && (
            <>
              <span aria-hidden>&middot;</span>
              <span className="max-w-32 truncate font-mono">{mission.top_model}</span>
            </>
          )}
          <span aria-hidden>&middot;</span>
          <span title={new Date(mission.created_at).toLocaleString()} className="whitespace-nowrap">
            {relativeTime(mission.created_at)}
          </span>
          {nextRun && (
            <>
              <span aria-hidden>&middot;</span>
              <span className="whitespace-nowrap">Next run {nextRun}</span>
            </>
          )}
          {cost && (
            <>
              <span aria-hidden>&middot;</span>
              <span className="whitespace-nowrap tabular-nums">{cost}</span>
            </>
          )}
        </div>
        {mission.pause_message && (
          <p className="mt-2 line-clamp-2 text-xs text-warning">{mission.pause_message}</p>
        )}
      </Link>
    </Card>
  )
}
