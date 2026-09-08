import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import {
  deleteDestination,
  listConnectors,
  listDestinations,
  patchDestination,
  testDestination,
} from '../../api/client'
import type { AdminConnector, Destination } from '../../api/types'
import { Button } from '../ui/button'
import { Switch } from '../ui/switch'
import { ConfirmDialog } from '../timothy/confirm-dialog'
import { EmptyState } from '../timothy/empty-state'
import { PageHeader, SectionHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { DestinationKindIcon } from '../destinations/DestinationKindIcon'
import { AddPresetTile } from './AddPresetTile'
import { EntityCard } from './EntityCard'
import { destinationPresets } from './destinationPresets'
import { settingsArea } from './settingsAreas'
import { TestStatus } from './TestStatus'
import { errText } from '../../lib/errors'

const area = settingsArea('destinations')

export function DestinationsList() {
  const [destinations, setDestinations] = useState<Destination[]>([])
  const [connectors, setConnectors] = useState<AdminConnector[]>([])

  const refresh = useCallback(() => {
    listDestinations()
      .then(setDestinations)
      .catch((err: unknown) => toast.error('Could not load destinations', { description: errText(err) }))
  }, [])
  useEffect(refresh, [refresh])

  // Resolves a github destination's connector id to its name for the
  // card summary; best-effort, the summary falls back to mode alone.
  useEffect(() => {
    listConnectors()
      .then(setConnectors)
      .catch(() => {
        // Non-fatal: card summaries just show mode without a connector name.
      })
  }, [])

  return (
    <PageShell>
      <PageHeader
        title={area.label}
        description={area.description}
        breadcrumbs={[{ label: 'Settings', href: '/settings' }, { label: area.label }]}
      />
      <div className="space-y-10">
        <section className="space-y-4">
          <SectionHeader title={destinations.length > 0 ? `Your destinations · ${destinations.length}` : 'Your destinations'} />
          <p className="-mt-2 max-w-2xl text-sm text-muted-foreground">
            Where mission results go. Attach one or more to a mission and its outcome digest
            delivers there once it finishes.
          </p>
          {destinations.length === 0 ? (
            <EmptyState title="No destinations yet" description="Add one below." />
          ) : (
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {destinations.map((d) => (
                <DestinationCard key={d.id} destination={d} connectors={connectors} onChanged={refresh} />
              ))}
            </div>
          )}
        </section>

        <section className="space-y-4">
          <SectionHeader title="Add a destination" />
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {destinationPresets.map((preset) => (
              <AddPresetTile
                key={preset.id}
                to={`/settings/destinations/new/${preset.id}`}
                title={preset.name}
                description={preset.description}
                tile={<DestinationKindIcon kind={preset.id} className="size-9" />}
              />
            ))}
          </div>
        </section>
      </div>
    </PageShell>
  )
}

// githubSummary renders a github destination's mode plus the
// connector name when it can be resolved cheaply from the already-
// loaded connector list, else the mode alone.
function githubSummary(destination: Destination, connectors: AdminConnector[]): string {
  const mode = destination.config.mode === 'push_pr' ? 'push + PR' : 'push'
  const connector = connectors.find((c) => c.id === destination.config.connector_id)
  return connector ? `${mode} via ${connector.name}` : mode
}

function DestinationCard({
  destination,
  connectors,
  onChanged,
}: {
  destination: Destination
  connectors: AdminConnector[]
  onChanged: () => void
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

  const summary =
    destination.kind === 'email'
      ? String(destination.config.to ?? '')
      : destination.kind === 'telegram'
        ? `chat ${String(destination.config.chat_id ?? '')}`
        : destination.kind === 'github'
          ? githubSummary(destination, connectors)
          : String(destination.config.url ?? '')

  return (
    <>
      <EntityCard
        to={`/settings/destinations/${destination.id}`}
        title={destination.name}
        tile={<DestinationKindIcon kind={destination.kind} className="size-9" />}
        summary={<div className="truncate text-xs text-muted-foreground">{summary}</div>}
        status={
          test && (
            <TestStatus
              state={test.ok ? 'ok' : 'failed'}
              message={test.ok ? 'Test delivery sent' : `Failed: ${test.error}`}
            />
          )
        }
        footer={
          <>
            <Switch checked={destination.enabled} onCheckedChange={toggle} aria-label={`${destination.name} enabled`} />
            {destination.kind !== 'github' && (
              <Button size="sm" variant="test" disabled={testing} onClick={() => void runTest()} className="flex-1">
                {testing ? 'Sending…' : 'Test send'}
              </Button>
            )}
            <Button size="sm" variant="destructive" onClick={() => setConfirmDelete(true)}>
              Delete
            </Button>
          </>
        }
      />
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete ${destination.name}?`}
        description="Removes the destination. Refused while any in-progress mission still delivers to it."
        confirmLabel="Delete"
        destructive
        onConfirm={() => void remove()}
      />
    </>
  )
}
