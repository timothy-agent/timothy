import { Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { toast } from 'sonner'
import { deleteSchedule, listDestinations, patchSchedule, listSchedules } from '../api/client'
import type { Destination, Schedule } from '../api/types'
import { describeCron } from '../lib/schedules'
import { relativeTime, relativeTimeUntil } from '../lib/format'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../components/ui/dialog'
import { Toggle } from '../components/settings/shared'
import { errText } from '../components/settings/util'

// Automations lists every recurring schedule as a card, folding in what
// RecurringSchedules used to render inline on the Missions page — same
// API calls (listSchedules/patchSchedule/deleteSchedule), moved here
// since a schedule now has its own detail page (run history) to link
// into.
export function Automations() {
  const navigate = useNavigate()
  const [schedules, setSchedules] = useState<Schedule[]>([])
  const [confirmDelete, setConfirmDelete] = useState<Schedule | null>(null)
  // Destinations fetched once per page, just to resolve a schedule's
  // destination_ids into display names for the badges below — best
  // effort, an empty/failed fetch just renders no badges.
  const [destinations, setDestinations] = useState<Destination[]>([])
  useEffect(() => {
    listDestinations()
      .then(setDestinations)
      .catch(() => {
        // Non-fatal: badges just don't render if this fails.
      })
  }, [])

  const refresh = useCallback(() => {
    listSchedules()
      .then(setSchedules)
      .catch((err: unknown) => toast.error('Could not load schedules', { description: errText(err) }))
  }, [])
  useEffect(refresh, [refresh])

  const toggle = (sc: Schedule, enabled: boolean) => {
    setSchedules((prev) => prev.map((s) => (s.id === sc.id ? { ...s, enabled } : s))) // optimistic
    patchSchedule(sc.id, { enabled }).then(refresh, (err: unknown) => {
      toast.error('Could not update schedule', { description: errText(err) })
      refresh()
    })
  }

  const remove = async () => {
    if (!confirmDelete) return
    try {
      await deleteSchedule(confirmDelete.id)
      toast.success('Schedule removed')
      setConfirmDelete(null)
      refresh()
    } catch (err) {
      toast.error('Could not remove schedule', { description: errText(err) })
      setConfirmDelete(null)
    }
  }

  return (
    <div className="mx-auto w-full max-w-full px-8 py-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">Automations</h1>
        <p className="text-sm text-muted-foreground">Recurring missions that run on a schedule.</p>
      </div>

      <div className="mt-6 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {schedules.map((sc) => (
          <div
            key={sc.id}
            className="flex flex-col gap-2 rounded-xl border border-border bg-card p-4 shadow-sm transition hover:border-brand hover:shadow-md"
          >
            <Link to={`/automations/${sc.id}`} className="min-w-0">
              <span className="truncate text-sm font-semibold">{sc.name}</span>
              <p className="mt-0.5 line-clamp-1 text-xs text-muted-foreground">
                {sc.mission_template.goal}
              </p>
              <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
                <span>{describeCron(sc.cron)}</span>
                {sc.next_run && <span>next {relativeTimeUntil(sc.next_run)}</span>}
                {sc.last_run && <span>last {relativeTime(sc.last_run)}</span>}
              </div>
              {sc.mission_template.destination_ids && sc.mission_template.destination_ids.length > 0 && (
                <div className="mt-1.5 flex flex-wrap gap-1">
                  {sc.mission_template.destination_ids.map((id) => {
                    const d = destinations.find((d) => d.id === id)
                    return (
                      <Badge key={id} variant="outline" className="text-xs">
                        {d?.name ?? id}
                      </Badge>
                    )
                  })}
                </div>
              )}
            </Link>
            <div className="flex items-center gap-2">
              <Toggle on={sc.enabled} onChange={(v) => toggle(sc, v)} label={`${sc.name} enabled`} />
              <Button
                size="sm"
                variant="outline"
                onClick={() => navigate(`/automations/${sc.id}/edit`)}
              >
                Edit
              </Button>
              <button
                type="button"
                aria-label={`Delete ${sc.name}`}
                onClick={() => setConfirmDelete(sc)}
                className="ml-auto text-muted-foreground hover:text-destructive"
              >
                <HugeiconsIcon icon={Delete02Icon} className="size-4" />
              </button>
            </div>
          </div>
        ))}
        {schedules.length === 0 && (
          <div className="col-span-full rounded-xl border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
            No automations yet. Create a mission and choose "Repeat on schedule" to add one.
          </div>
        )}
      </div>

      <Dialog open={confirmDelete !== null} onOpenChange={(o) => !o && setConfirmDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {confirmDelete?.name}?</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            This schedule stops firing. Missions it already created keep their history.
          </p>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmDelete(null)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => void remove()}>
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
