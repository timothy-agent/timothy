import { CalendarClock } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { deleteSchedule, listDestinations, patchSchedule, listSchedules } from '../api/client'
import type { Destination, Schedule } from '../api/types'
import { ScheduleCard } from '../components/missions/ScheduleCard'
import { ConfirmDialog } from '../components/timothy/confirm-dialog'
import { EmptyState } from '../components/timothy/empty-state'
import { PageHeader } from '../components/timothy/page-header'
import { PageShell } from '../components/timothy/page-shell'
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
    <PageShell>
      <PageHeader title="Automations" description="Recurring missions that run on a schedule." />

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {schedules.map((sc) => (
          <ScheduleCard
            key={sc.id}
            schedule={sc}
            destinations={destinations}
            onToggle={(enabled) => toggle(sc, enabled)}
            onEdit={() => navigate(`/automations/${sc.id}/edit`)}
            onDelete={() => setConfirmDelete(sc)}
          />
        ))}
        {schedules.length === 0 && (
          <div className="col-span-full rounded-md border border-dashed border-border">
            <EmptyState
              icon={CalendarClock}
              title="No automations yet"
              description='Create a mission and choose "Repeat on schedule" to add one.'
            />
          </div>
        )}
      </div>

      <ConfirmDialog
        open={confirmDelete !== null}
        onOpenChange={(o) => !o && setConfirmDelete(null)}
        title={`Delete ${confirmDelete?.name}?`}
        description="This schedule stops firing. Missions it already created keep their history."
        confirmLabel="Delete"
        destructive
        onConfirm={() => void remove()}
      />
    </PageShell>
  )
}
