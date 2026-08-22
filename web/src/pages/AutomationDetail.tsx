import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { listDestinations, listMissions, listSchedules } from '../api/client'
import type { Destination, Mission, Schedule } from '../api/types'
import { MissionCard } from '../components/missions/MissionCard'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import { describeCron } from '../lib/schedules'
import { relativeTime, relativeTimeUntil } from '../lib/format'

// AutomationDetail shows one schedule's summary plus the missions it
// has fired — no GET-by-id for schedules, same as EditSchedule, so the
// list (small) is searched for the one being viewed.
export function AutomationDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [schedule, setSchedule] = useState<Schedule | null>(null)
  const [loading, setLoading] = useState(true)
  const [missions, setMissions] = useState<Mission[]>([])
  // Destinations fetched once per page, just to resolve
  // destination_ids into display names for the badges below.
  const [destinations, setDestinations] = useState<Destination[]>([])
  useEffect(() => {
    listDestinations()
      .then(setDestinations)
      .catch(() => {
        // Non-fatal: badges just don't render if this fails.
      })
  }, [])

  const refresh = useCallback(() => {
    if (!id) return
    listSchedules().then(
      (rows) => {
        setSchedule(rows.find((s) => s.id === id) ?? null)
        setLoading(false)
      },
      () => setLoading(false),
    )
    listMissions({ scheduleId: id }).then(setMissions, () => undefined)
  }, [id])

  useEffect(refresh, [refresh])

  if (!id) return null

  if (loading) {
    return (
      <div className="mx-auto w-full max-w-full px-8 py-6">
        <p className="text-sm text-muted-foreground">Loading…</p>
      </div>
    )
  }

  if (!schedule) {
    return (
      <div className="mx-auto w-full max-w-full px-8 py-6">
        <p className="text-sm text-muted-foreground">
          Automation not found.{' '}
          <Link to="/automations" className="underline underline-offset-2 hover:text-foreground">
            Back to automations
          </Link>
        </p>
      </div>
    )
  }

  return (
    <div className="mx-auto w-full max-w-full px-8 py-6">
      <div className="flex items-center justify-between gap-4">
        <div className="min-w-0">
          <h1 className="truncate text-xl font-semibold tracking-tight">{schedule.name}</h1>
          <p className="line-clamp-1 text-sm text-muted-foreground">{schedule.mission_template.goal}</p>
          <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
            <span>{describeCron(schedule.cron)}</span>
            {schedule.next_run && <span>next {relativeTimeUntil(schedule.next_run)}</span>}
            {schedule.last_run && <span>last {relativeTime(schedule.last_run)}</span>}
            <span>{schedule.enabled ? 'enabled' : 'disabled'}</span>
          </div>
          {schedule.mission_template.destination_ids &&
            schedule.mission_template.destination_ids.length > 0 && (
              <div className="mt-1.5 flex flex-wrap gap-1">
                {schedule.mission_template.destination_ids.map((did) => {
                  const d = destinations.find((d) => d.id === did)
                  return (
                    <Badge key={did} variant="outline" className="text-xs">
                      {d?.name ?? did}
                    </Badge>
                  )
                })}
              </div>
            )}
        </div>
        <Button variant="outline" onClick={() => navigate(`/automations/${id}/edit`)}>
          Edit
        </Button>
      </div>

      <div className="mt-6 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {missions.map((m) => (
          <MissionCard key={m.id} mission={m} />
        ))}
        {missions.length === 0 && (
          <div className="col-span-full rounded-xl border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
            No missions fired yet.
          </div>
        )}
      </div>
    </div>
  )
}
