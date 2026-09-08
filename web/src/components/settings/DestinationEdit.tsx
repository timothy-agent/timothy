import { Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import {
  deleteDestination,
  listConnectors,
  listDestinations,
  patchDestination,
  setSecret,
  testDestination,
} from '../../api/client'
import type { AdminConnector, Destination } from '../../api/types'
import { Button } from '../ui/button'
import { Switch } from '../ui/switch'
import { Alert, AlertDescription } from '../ui/alert'
import { ConfirmDialog } from '../timothy/confirm-dialog'
import { FieldGroup, Form, FormActions } from '../timothy/field'
import { Panel } from '../timothy/panel'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { CredentialField, type CredentialMode } from './CredentialRefPicker'
import { DestinationKindFields, type DestinationKindValues } from './DestinationKindFields'
import { settingsArea } from './settingsAreas'
import { TestStatus } from './TestStatus'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'
import { useStagedForm } from './useStagedForm'
import { errText } from './util'

const area = settingsArea('destinations')

function valuesFrom(destination: Destination): DestinationKindValues {
  return {
    connectorID: String(destination.config.connector_id ?? ''),
    to: String(destination.config.to ?? ''),
    url: String(destination.config.url ?? ''),
    format: (destination.config.format as 'json' | 'text') ?? 'json',
    chatID: String(destination.config.chat_id ?? ''),
    mode: (destination.config.mode as 'push' | 'push_pr') ?? 'push',
    branchPattern: String(destination.config.branch_pattern ?? ''),
    commitStyle: String(destination.config.commit_style ?? ''),
    createIfMissing: Boolean(destination.config.create_if_missing),
  }
}

// buildConfig builds the destination's config PATCH body from the
// staged kind fields, same shape the old per-kind config builders sent.
function buildConfig(destination: Destination, values: DestinationKindValues): Record<string, unknown> {
  if (destination.kind === 'email') return { connector_id: values.connectorID, to: values.to.trim() }
  if (destination.kind === 'telegram') return { chat_id: values.chatID.trim() }
  if (destination.kind === 'github') {
    return {
      connector_id: values.connectorID,
      mode: values.mode,
      branch_pattern: values.branchPattern.trim() || undefined,
      commit_style: values.commitStyle || undefined,
      create_if_missing: values.createIfMissing || undefined,
    }
  }
  return { url: values.url.trim(), format: values.format }
}

// DestinationEdit loads the destination, then hands off to
// DestinationEditForm keyed by its id: a fresh mount per destination so
// useStagedForm's baseline is never initialized from a placeholder
// before the real data arrives.
export function DestinationEdit() {
  const { id } = useParams()
  const [destination, setDestination] = useState<Destination | null | undefined>(undefined)
  const [connectors, setConnectors] = useState<AdminConnector[]>([])

  const refresh = useCallback(() => {
    return listDestinations()
      .then((list) => {
        const found = list.find((d) => d.id === id) ?? null
        setDestination(found)
        return found
      })
      .catch((err: unknown) => {
        toast.error('Could not load destination', { description: errText(err) })
        return undefined
      })
  }, [id])
  useEffect(() => {
    void refresh()
  }, [refresh])

  useEffect(() => {
    listConnectors()
      .then((rows) => setConnectors(rows.filter((c) => (c.kind === 'google' || c.kind === 'github') && c.enabled)))
      .catch(() => {
        // Non-fatal: the connector select just shows the currently
        // stored id with no friendly name if this fails.
      })
  }, [])

  if (destination === null) return <Navigate to="/settings/destinations" replace />
  if (destination === undefined) return null

  return (
    <DestinationEditForm
      key={destination.id}
      initialDestination={destination}
      connectors={connectors}
      refresh={refresh}
    />
  )
}

function DestinationEditForm({
  initialDestination,
  connectors,
  refresh,
}: {
  initialDestination: Destination
  connectors: AdminConnector[]
  refresh: () => Promise<Destination | null | undefined>
}) {
  const navigate = useNavigate()
  const defaultBackend = useDefaultSecretBackend()

  const [destination, setDestinationState] = useState(initialDestination)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [test, setTest] = useState<{ ok: boolean; error?: string } | null>(null)
  const [testing, setTesting] = useState(false)

  const staged = useStagedForm<DestinationKindValues>(valuesFrom(initialDestination))
  const setField = <K extends keyof DestinationKindValues>(key: K, value: DestinationKindValues[K]) =>
    staged.setField(key, value)

  // Rotating the bot token is a separate save from the rest of the
  // config: it writes credential_ref's value, not config.
  const [botToken, setBotToken] = useState('')
  const [botTokenMode, setBotTokenMode] = useState<CredentialMode>('new')
  const [existingBotTokenRef, setExistingBotTokenRef] = useState('')
  const [savingToken, setSavingToken] = useState(false)

  const doRefresh = useCallback(async () => {
    const refetched = await refresh()
    if (refetched) setDestinationState(refetched)
    return refetched
  }, [refresh])

  const save = useCallback(async () => {
    setSaving(true)
    setSaveError(null)
    try {
      const config = buildConfig(destination, staged.values)
      await patchDestination(destination.id, { config })
      toast.success('Destination updated')
      const refetched = await doRefresh()
      if (refetched) staged.rebase(valuesFrom(refetched))
    } catch (err) {
      setSaveError(errText(err))
    } finally {
      setSaving(false)
    }
  }, [destination, doRefresh, staged])

  const remove = async () => {
    try {
      await deleteDestination(destination.id)
      toast.success('Destination removed')
      navigate('/settings/destinations')
    } catch (err) {
      toast.error('Could not remove destination', { description: errText(err) })
      setConfirmDelete(false)
    }
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

  const toggleEnabled = (enabled: boolean) => {
    patchDestination(destination.id, { enabled })
      .then(() => void doRefresh())
      .catch((err: unknown) => toast.error('Could not update destination', { description: errText(err) }))
  }

  const usingExistingBotToken = botTokenMode === 'existing'

  const saveBotToken = async () => {
    setSavingToken(true)
    try {
      const ref = usingExistingBotToken ? existingBotTokenRef : destination.credential_ref
      if (!usingExistingBotToken) await setSecret(ref, botToken.trim())
      await patchDestination(destination.id, { credential_ref: ref })
      toast.success('Bot token updated')
      setBotToken('')
      void doRefresh()
    } catch (err) {
      toast.error('Could not update bot token', { description: errText(err) })
    } finally {
      setSavingToken(false)
    }
  }

  return (
    <PageShell width="form">
      <PageHeader
        title={destination.name}
        description={destination.kind}
        breadcrumbs={[
          { label: 'Settings', href: '/settings' },
          { label: area.label, href: '/settings/destinations' },
          { label: destination.name },
        ]}
        actions={
          <Button variant="destructive" onClick={() => setConfirmDelete(true)}>
            <HugeiconsIcon icon={Delete02Icon} />
            Delete
          </Button>
        }
      />

      <div className="mb-8 flex items-center justify-between gap-4 rounded-md border border-border p-4">
        <div className="min-w-0">
          <div className="text-sm font-medium">Enabled</div>
          <p className="text-sm text-muted-foreground">
            Disabled destinations are skipped by mission delivery without an error.
          </p>
        </div>
        <Switch checked={destination.enabled} onCheckedChange={toggleEnabled} aria-label={`${destination.name} enabled`} />
      </div>

      <Form
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <FieldGroup>
          <DestinationKindFields kind={destination.kind} values={staged.values} setField={setField} connectors={connectors} />
        </FieldGroup>

        {saveError && (
          <Alert tone="destructive">
            <AlertDescription className="flex flex-wrap items-center justify-between gap-3">
              <span>Could not save destination changes: {saveError}</span>
              <Button type="button" size="sm" variant="outline" onClick={() => void save()}>
                Retry
              </Button>
            </AlertDescription>
          </Alert>
        )}

        <FormActions note={staged.dirty ? 'Unsaved changes' : undefined}>
          <Button type="button" variant="outline" disabled={saving} onClick={staged.reset}>
            Cancel
          </Button>
          <Button type="submit" disabled={!staged.dirty || saving}>
            Save
          </Button>
        </FormActions>
      </Form>

      <div className="mt-10 space-y-4">
        {destination.kind !== 'github' && (
          <Panel title="Test send">
            {test || testing ? (
              <TestStatus
                state={testing ? 'testing' : test?.ok ? 'ok' : 'failed'}
                message={testing ? 'Sending test delivery…' : test?.ok ? 'Test delivery sent.' : `Failed: ${test?.error}`}
                action={
                  !testing && (
                    <Button size="sm" variant="test" onClick={() => void runTest()}>
                      Test send
                    </Button>
                  )
                }
              />
            ) : (
              <div className="flex flex-wrap items-center gap-3 rounded-md border border-border bg-muted/40 p-4 text-sm text-muted-foreground">
                <span className="min-w-0 flex-1 font-medium">Not tested yet.</span>
                <Button size="sm" variant="test" onClick={() => void runTest()}>
                  Test send
                </Button>
              </div>
            )}
          </Panel>
        )}

        {destination.kind === 'telegram' && (
          <Panel title="Rotate bot token">
            <div className="space-y-3">
              <CredentialField
                label="Bot token"
                mode={botTokenMode}
                onModeChange={(m) => {
                  setBotTokenMode(m)
                  if (m === 'existing' && !existingBotTokenRef) setExistingBotTokenRef(destination.credential_ref)
                }}
                existingRef={existingBotTokenRef}
                onExistingRefChange={setExistingBotTokenRef}
                secretValue={botToken}
                onSecretValueChange={setBotToken}
                secretPlaceholder="123456:ABC-DEF..."
                defaultBackend={defaultBackend}
                refName={destination.credential_ref}
                modeLabels={{ new: 'New token', existing: 'Different credential' }}
              />
              <Button
                size="sm"
                disabled={savingToken || (usingExistingBotToken ? !existingBotTokenRef : !botToken.trim())}
                onClick={() => void saveBotToken()}
              >
                {savingToken ? 'Saving…' : 'Save token'}
              </Button>
            </div>
          </Panel>
        )}
      </div>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete ${destination.name}?`}
        description="Removes the destination. Refused while any in-progress mission still delivers to it."
        confirmLabel="Delete"
        destructive
        onConfirm={() => void remove()}
      />
    </PageShell>
  )
}
