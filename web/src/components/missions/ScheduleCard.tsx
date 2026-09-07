import { Trash2 } from 'lucide-react'
import { Link } from 'react-router'
import type { Destination, Schedule } from '../../api/types'
import { relativeTime, relativeTimeUntil } from '../../lib/format'
import { describeCron } from '../../lib/schedules'
import { DestinationKindIcon } from '../destinations/DestinationKindIcon'
import { IconButton } from '../timothy/icon-button'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Card } from '../ui/card'
import { Switch } from '../ui/switch'

// ScheduleCard renders a schedule with the same anatomy as MissionCard
// (14.9 "Automations reuse MissionCard with a Next run metadata
// line"), but a schedule is not a mission and carries its own controls
// (enable switch, Edit, Delete) that must stay reachable outside the
// card's link target, kept in a separate footer rather than the whole
// card, since it hosts more than one interactive target. Manageable-
// resource exception to "one interactive target per card" (12): the
// title link is the only navigational target, the footer below the
// border-t divider holds the non-navigational controls.
export function ScheduleCard({
  schedule,
  destinations,
  onToggle,
  onEdit,
  onDelete,
}: {
  schedule: Schedule
  destinations: Destination[]
  onToggle: (enabled: boolean) => void
  onEdit: () => void
  onDelete: () => void
}) {
  const destinationIds = schedule.mission_template.destination_ids ?? []

  return (
    <Card>
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge variant="secondary" size="sm">
          recurring
        </Badge>
        <Badge variant={schedule.enabled ? 'good' : 'neutral'} size="sm">
          {schedule.enabled ? 'enabled' : 'disabled'}
        </Badge>
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
      <Link to={`/automations/${schedule.id}`} className="hover:underline">
        <p className="mt-3 line-clamp-2 text-sm font-medium">{schedule.name}</p>
      </Link>
      <p className="mt-1 line-clamp-1 text-xs text-muted-foreground">{schedule.mission_template.goal}</p>
      <div className="mt-2 flex flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs text-muted-foreground">
        <span className="whitespace-nowrap">{describeCron(schedule.cron)}</span>
        {schedule.next_run && (
          <>
            <span aria-hidden>&middot;</span>
            <span className="whitespace-nowrap">Next run {relativeTimeUntil(schedule.next_run)}</span>
          </>
        )}
        {schedule.last_run && (
          <>
            <span aria-hidden>&middot;</span>
            <span className="whitespace-nowrap">Last run {relativeTime(schedule.last_run)}</span>
          </>
        )}
      </div>
      <div className="mt-4 flex items-center gap-2 border-t border-border pt-3">
        <Switch checked={schedule.enabled} onCheckedChange={onToggle} aria-label={`${schedule.name} enabled`} />
        <Button size="sm" variant="outline" onClick={onEdit}>
          Edit
        </Button>
        <IconButton
          label={`Delete ${schedule.name}`}
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
