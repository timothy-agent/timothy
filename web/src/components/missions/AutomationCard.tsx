import { Trash2 } from 'lucide-react'
import { Link } from 'react-router'
import type { Automation, Destination } from '../../api/types'
import { relativeTime, relativeTimeUntil } from '../../lib/format'
import { cronExpr, describeCron } from '../../lib/cron'
import { DestinationKindIcon } from '../destinations/DestinationKindIcon'
import { IconButton } from '../timothy/icon-button'
import { automationRunStatus } from '../timothy/status'
import { StatusBadge } from '../timothy/status-badge'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Card } from '../ui/card'
import { Switch } from '../ui/switch'

// AutomationCard renders an automation with the same anatomy as MissionCard
// (14.9 "Automations reuse MissionCard with a Next run metadata
// line"), but an automation is not a mission and carries its own controls
// (enable switch, Edit, Delete) that must stay reachable outside the
// card's link target, kept in a separate footer rather than the whole
// card, since it hosts more than one interactive target. Manageable-
// resource exception to "one interactive target per card" (12): the
// title link is the only navigational target, the footer below the
// border-t divider holds the non-navigational controls.
export function AutomationCard({
  automation,
  destinations,
  onToggle,
  onEdit,
  onDelete,
}: {
  automation: Automation
  destinations: Destination[]
  onToggle: (enabled: boolean) => void
  onEdit: () => void
  onDelete: () => void
}) {
  const destinationIds = automation.action.mission.destination_ids ?? []
  const cron = cronExpr(automation)
  const { next_run_at: nextRun, last_run_at: lastRun, last_run_status: lastStatus } = automation.stats

  return (
    <Card>
      <div className="flex flex-wrap items-center gap-1.5">
        {cron && (
          <Badge variant="secondary" size="sm">
            recurring
          </Badge>
        )}
        <Badge variant={automation.enabled ? 'good' : 'neutral'} size="sm">
          {automation.enabled ? 'enabled' : 'disabled'}
        </Badge>
        {lastStatus && (
          <StatusBadge status={automationRunStatus(lastStatus)} label={`last run ${lastStatus}`} size="sm" />
        )}
        {destinationIds.map((id) => {
          const d = destinations.find((d) => d.id === id)
          return (
            <Badge key={id} variant="outline" size="sm">
              {d && <DestinationKindIcon kind={d.kind} />}
              {d?.name ?? id}
            </Badge>
          )
        })}
      </div>
      <Link to={`/automations/${automation.id}`} className="hover:underline">
        <p className="mt-3 line-clamp-2 text-sm font-medium">{automation.name}</p>
      </Link>
      <p className="mt-1 line-clamp-1 text-xs text-muted-foreground">{automation.action.mission.goal}</p>
      <div className="mt-2 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs text-muted-foreground">
        {cron && <span className="whitespace-nowrap">{describeCron(cron)}</span>}
        {nextRun && (
          <>
            {cron && <span aria-hidden>&middot;</span>}
            <span className="whitespace-nowrap">Next run {relativeTimeUntil(nextRun)}</span>
          </>
        )}
        {lastRun && (
          <>
            {(cron || nextRun) && <span aria-hidden>&middot;</span>}
            <span className="whitespace-nowrap">Last run {relativeTime(lastRun)}</span>
          </>
        )}
      </div>
      <div className="mt-4 flex items-center gap-2 border-t border-border pt-3">
        <Switch checked={automation.enabled} onCheckedChange={onToggle} aria-label={`${automation.name} enabled`} />
        <Button size="sm" variant="outline" onClick={onEdit}>
          Edit
        </Button>
        <IconButton
          label={`Delete ${automation.name}`}
          icon={Trash2}
          variant="ghost"
          size="sm"
          className="ml-auto"
          onClick={onDelete}
        />
      </div>
    </Card>
  )
}
