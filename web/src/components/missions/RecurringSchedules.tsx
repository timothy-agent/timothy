import { Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { deleteSchedule, patchSchedule, listSchedules } from '../../api/client'
import type { Schedule } from '../../api/types'
import { describeCron } from '../../lib/schedules'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { Toggle } from '../settings/shared'
import { errText } from '../settings/util'

function formatDate(v?: string): string {
  if (!v) return 'N/A'
  return new Date(v).toLocaleString()
}

// RecurringSchedules lists the recurring missions created from the new
// mission page's "Repeat on schedule" option — rendered on the
// Missions page between the notifications strip and the mission grid,
// only when at least one schedule exists.
export function RecurringSchedules() {
  const navigate = useNavigate()
  const [schedules, setSchedules] = useState<Schedule[]>([])
  const [confirmDelete, setConfirmDelete] = useState<Schedule | null>(null)

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

  if (schedules.length === 0) return null

  return (
    <div className="mt-4 space-y-3">
      <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Recurring · {schedules.length}
      </h2>
      <div className="space-y-3">
        {schedules.map((sc) => (
          <div
            key={sc.id}
            className="flex items-center gap-4 rounded-xl border border-border bg-card p-4"
          >
            <div className="min-w-0 flex-1">
              <span className="truncate text-sm font-semibold">{sc.name}</span>
              <p className="mt-0.5 line-clamp-1 text-xs text-muted-foreground">
                {sc.mission_template.goal}
              </p>
              <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
                <span>{describeCron(sc.cron)}</span>
                <span>next run {formatDate(sc.next_run)}</span>
                {sc.last_run && <span>last run {formatDate(sc.last_run)}</span>}
              </div>
            </div>
            <Toggle on={sc.enabled} onChange={(v) => toggle(sc, v)} label={`${sc.name} enabled`} />
            <Button
              size="sm"
              variant="outline"
              onClick={() => navigate(`/missions/schedules/${sc.id}/edit`)}
            >
              Edit
            </Button>
            <button
              type="button"
              aria-label={`Delete ${sc.name}`}
              onClick={() => setConfirmDelete(sc)}
              className="text-muted-foreground hover:text-destructive"
            >
              <HugeiconsIcon icon={Delete02Icon} className="size-4" />
            </button>
          </div>
        ))}
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
