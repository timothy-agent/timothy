import { CalendarClock } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { deleteAutomation, listAutomations, listDestinations, patchAutomation } from '../api/client'
import type { Automation, Destination } from '../api/types'
import { AutomationCard } from '../components/missions/AutomationCard'
import { ConfirmDialog } from '../components/timothy/confirm-dialog'
import { EmptyState } from '../components/timothy/empty-state'
import { PageHeader } from '../components/timothy/page-header'
import { PageShell } from '../components/timothy/page-shell'
import { errText } from '../lib/errors'

// Automations lists every automation as a card; each links to its own
// detail page (run history).
export function Automations() {
  const navigate = useNavigate()
  const [automations, setAutomations] = useState<Automation[]>([])
  const [confirmDelete, setConfirmDelete] = useState<Automation | null>(null)
  // Destinations fetched once per page, just to resolve an automation's
  // destination_ids into display names for the badges below: best
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
    listAutomations()
      .then(setAutomations)
      .catch((err: unknown) => toast.error('Could not load automations', { description: errText(err) }))
  }, [])
  useEffect(refresh, [refresh])

  const toggle = (au: Automation, enabled: boolean) => {
    setAutomations((prev) => prev.map((a) => (a.id === au.id ? { ...a, enabled } : a))) // optimistic
    patchAutomation(au.id, { enabled }).then(refresh, (err: unknown) => {
      toast.error('Could not update automation', { description: errText(err) })
      refresh()
    })
  }

  const remove = async () => {
    if (!confirmDelete) return
    try {
      await deleteAutomation(confirmDelete.id)
      toast.success('Automation removed')
      setConfirmDelete(null)
      refresh()
    } catch (err) {
      toast.error('Could not remove automation', { description: errText(err) })
      setConfirmDelete(null)
    }
  }

  return (
    <PageShell>
      <PageHeader title="Automations" description="Recurring missions that run on a cron." />

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {automations.map((au) => (
          <AutomationCard
            key={au.id}
            automation={au}
            destinations={destinations}
            onToggle={(enabled) => toggle(au, enabled)}
            onEdit={() => navigate(`/automations/${au.id}/edit`)}
            onDelete={() => setConfirmDelete(au)}
          />
        ))}
        {automations.length === 0 && (
          <div className="col-span-full rounded-md border border-dashed border-border">
            <EmptyState
              icon={CalendarClock}
              title="No automations yet"
              description='Create a mission and choose "Repeat on a cron" to add one.'
            />
          </div>
        )}
      </div>

      <ConfirmDialog
        open={confirmDelete !== null}
        onOpenChange={(o) => !o && setConfirmDelete(null)}
        title={`Delete ${confirmDelete?.name}?`}
        description="This automation stops running. Missions it already created keep their history."
        confirmLabel="Delete"
        destructive
        onConfirm={() => void remove()}
      />
    </PageShell>
  )
}
