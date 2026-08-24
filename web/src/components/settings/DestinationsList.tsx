import { Mail01Icon, GlobalIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { TelegramIcon } from '@/components/icons/TelegramIcon'
import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { deleteDestination, listDestinations, patchDestination, testDestination } from '../../api/client'
import type { Destination } from '../../api/types'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { Toggle } from './shared'
import { errText } from './util'

const kindIcon = { email: Mail01Icon, webhook: GlobalIcon } as const

export function DestinationsList() {
  const [destinations, setDestinations] = useState<Destination[]>([])
  const navigate = useNavigate()

  const refresh = useCallback(() => {
    listDestinations()
      .then(setDestinations)
      .catch((err: unknown) => toast.error('Could not load destinations', { description: errText(err) }))
  }, [])
  useEffect(refresh, [refresh])

  return (
    <div className="mt-6 space-y-8">
      <section className="space-y-3">
        <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {destinations.length > 0 ? `Your destinations · ${destinations.length}` : 'Your destinations'}
        </h2>
        <p className="text-sm text-muted-foreground">
          Where mission results go. Attach one or more to a mission and its outcome digest
          delivers there once it finishes.
        </p>
        {destinations.length === 0 ? (
          <div className="rounded-xl border border-dashed border-border p-10 text-center text-sm text-muted-foreground">
            No destinations yet, add one below.
          </div>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {destinations.map((d) => (
              <DestinationCard
                key={d.id}
                destination={d}
                onChanged={refresh}
                onManage={() => navigate(`/settings/destinations/${d.id}`)}
              />
            ))}
          </div>
        )}
      </section>

      <section className="space-y-3">
        <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Add a destination
        </h2>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <button
            type="button"
            onClick={() => navigate('/settings/destinations/new/email')}
            className="flex items-center gap-3 rounded-xl border border-dashed border-border p-4 text-left transition hover:border-brand hover:bg-muted/50"
          >
            <HugeiconsIcon icon={Mail01Icon} className="size-9" />
            <span className="min-w-0">
              <span className="block text-sm font-semibold">Email</span>
              <span className="block truncate text-sm text-muted-foreground">
                Sends via a connected Gmail account
              </span>
            </span>
          </button>
          <button
            type="button"
            onClick={() => navigate('/settings/destinations/new/webhook')}
            className="flex items-center gap-3 rounded-xl border border-dashed border-border p-4 text-left transition hover:border-brand hover:bg-muted/50"
          >
            <HugeiconsIcon icon={GlobalIcon} className="size-9" />
            <span className="min-w-0">
              <span className="block text-sm font-semibold">Webhook</span>
              <span className="block truncate text-sm text-muted-foreground">
                POSTs the digest as JSON or plain text
              </span>
            </span>
          </button>
          <button
            type="button"
            onClick={() => navigate('/settings/destinations/new/telegram')}
            className="flex items-center gap-3 rounded-xl border border-dashed border-border p-4 text-left transition hover:border-brand hover:bg-muted/50"
          >
            <TelegramIcon className="size-9" />
            <span className="min-w-0">
              <span className="block text-sm font-semibold">Telegram</span>
              <span className="block truncate text-sm text-muted-foreground">
                Sends via a bot to a chat
              </span>
            </span>
          </button>
        </div>
      </section>
    </div>
  )
}

function DestinationCard({
  destination,
  onChanged,
  onManage,
}: {
  destination: Destination
  onChanged: () => void
  onManage: () => void
}) {
  const [testing, setTesting] = useState(false)
  const [test, setTest] = useState<{ ok: boolean; error?: string } | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const toggle = (enabled: boolean) => {
    patchDestination(destination.id, { enabled }).then(onChanged, (err: unknown) =>
      toast.error('Could not update destination', { description: errText(err) }),
    )
  }

  const runTest = async () => {
    setTesting(true)
    setTest(null)
    try {
      setTest(await testDestination(destination.id))
    } catch (err) {
      setTest({ ok: false, error: errText(err) })
    } finally {
      setTesting(false)
    }
  }

  const remove = async () => {
    try {
      await deleteDestination(destination.id)
      toast.success('Destination removed')
      onChanged()
    } catch (err) {
      toast.error('Could not remove destination', { description: errText(err) })
    } finally {
      setConfirmDelete(false)
    }
  }

  return (
    <div className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4 shadow-sm transition hover:shadow-md">
      <div className="flex items-center gap-3">
        {destination.kind === 'telegram' ? (
          <TelegramIcon className="size-9" />
        ) : (
          <HugeiconsIcon icon={kindIcon[destination.kind]} className="size-9" />
        )}
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-semibold">{destination.name}</div>
          <div className="text-xs text-muted-foreground uppercase">{destination.kind}</div>
        </div>
        <Toggle on={destination.enabled} onChange={toggle} label={`${destination.name} enabled`} />
      </div>

      <div className="truncate text-xs text-muted-foreground">
        {destination.kind === 'email'
          ? String(destination.config.to ?? '')
          : destination.kind === 'telegram'
            ? `chat ${String(destination.config.chat_id ?? '')}`
            : String(destination.config.url ?? '')}
      </div>

      {test && (
        <div
          className={`rounded-lg border p-2 text-xs ${test.ok ? 'border-good/30 bg-good-soft text-good' : 'border-destructive/30 bg-destructive/5 text-destructive'}`}
        >
          {test.ok ? 'Test delivery sent' : `Failed: ${test.error}`}
        </div>
      )}

      <div className="mt-auto flex items-center gap-2 pt-1">
        <Button size="sm" variant="test" disabled={testing} onClick={() => void runTest()} className="flex-1">
          {testing ? 'Sending…' : 'Test send'}
        </Button>
        <Button size="sm" variant="outline" onClick={onManage} className="flex-1">
          Manage
        </Button>
        <Button size="sm" variant="destructive" onClick={() => setConfirmDelete(true)}>
          Delete
        </Button>
      </div>

      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {destination.name}?</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            Removes the destination. Refused while any in-progress mission still delivers to it.
          </p>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmDelete(false)}>
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
