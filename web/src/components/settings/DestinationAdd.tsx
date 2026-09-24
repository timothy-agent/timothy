import { useEffect, useState } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { createDestination, listChannels, listConnectors, patchDestination, testDestination } from '../../api/client'
import type { AdminConnector, Channel } from '../../api/types'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { Field, FieldGroup, Form, FormActions } from '../timothy/field'
import { channelDestinationConfig, channelDestinationReady } from './channelDestination'
import { DestinationKindFields, type DestinationKindValues } from './DestinationKindFields'
import { settingsArea } from './settingsAreas'
import { TestStatus } from './TestStatus'
import { errText } from '../../lib/errors'
import { gitKindMeta, isGitKind, type GitKind } from '../../lib/gitKinds'

const area = settingsArea('destinations')

// DestinationAdd is kind-aware (email vs webhook vs channel vs github)
// and its own page, mirroring ConnectorAdd's shape: the destination row
// is created (disabled) as part of testing, and a passing test enables
// it. GitHub has no test-send affordance, so it creates enabled
// directly. A channel test sends nothing: it checks the channel.
export function DestinationAdd() {
  const { kind } = useParams<{ kind: 'email' | 'webhook' | 'channel' | GitKind }>()
  const isRepoKind = !!kind && isGitKind(kind)
  const navigate = useNavigate()

  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [test, setTest] = useState<{ ok: boolean; error?: string } | null>(null)
  const [createdID, setCreatedID] = useState<string | null>(null)

  const [connectors, setConnectors] = useState<AdminConnector[] | null>(null)
  const [values, setValues] = useState<DestinationKindValues>({
    connectorID: '',
    to: '',
    url: '',
    format: 'json',
    chatID: '',
    threadID: '',
    channelID: '',
    mode: 'push',
    branchPattern: '',
    commitStyle: '',
    createIfMissing: false,
  })
  const setField = <K extends keyof DestinationKindValues>(key: K, value: DestinationKindValues[K]) => {
    setValues((prev) => ({ ...prev, [key]: value }))
    invalidate()
  }

  const [channels, setChannels] = useState<Channel[] | null>(null)
  useEffect(() => {
    if (kind !== 'channel') return
    listChannels()
      .then(setChannels)
      .catch((err: unknown) => toast.error('Could not load channels', { description: errText(err) }))
  }, [kind])

  useEffect(() => {
    if (kind !== 'email' && !(kind && isGitKind(kind))) return
    const wantKind = kind === 'email' ? 'google' : kind
    listConnectors()
      .then((rows) => setConnectors(rows.filter((c) => c.kind === wantKind && c.enabled)))
      .catch((err: unknown) => toast.error('Could not load connectors', { description: errText(err) }))
  }, [kind])

  if (kind !== 'email' && kind !== 'webhook' && kind !== 'channel' && !isRepoKind) {
    return <Navigate to="/settings/destinations" replace />
  }

  function invalidate() {
    setTest(null)
    setCreatedID(null)
  }

  const channel = channels?.find((c) => c.id === values.channelID)

  const config =
    kind === 'email'
      ? { connector_id: values.connectorID, to: values.to.trim() }
      : kind === 'channel'
        ? channelDestinationConfig(values, channel)
        : isRepoKind
          ? {
              connector_id: values.connectorID,
              mode: values.mode,
              branch_pattern: values.branchPattern.trim() || undefined,
              commit_style: values.commitStyle || undefined,
              create_if_missing: values.createIfMissing || undefined,
            }
          : { url: values.url.trim(), format: values.format }

  const canTest =
    name.trim() !== '' &&
    (kind === 'email'
      ? values.connectorID !== '' && values.to.trim() !== ''
      : kind === 'channel'
        ? channelDestinationReady(values, channel)
        : isRepoKind
          ? values.connectorID !== ''
          : values.url.trim() !== '')

  const runTest = async () => {
    setBusy(true)
    setTest(null)
    try {
      const id = await createDestination({ name: name.trim(), kind, config, enabled: false })
      setCreatedID(id)
      setTest(await testDestination(id))
    } catch (err) {
      setTest({ ok: false, error: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  // github destinations cannot be test-sent (no test-send affordance
  // for this kind), so adding one creates it enabled directly.
  const submitGitHub = async () => {
    if (!canTest) return
    setBusy(true)
    try {
      await createDestination({ name: name.trim(), kind, config, enabled: true })
      toast.success('Destination added')
      navigate('/settings/destinations')
    } catch (err) {
      toast.error('Could not add destination', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  const submit = async () => {
    if (!test?.ok || !createdID) return
    // The destination was created disabled to test it; enabling it is
    // a separate concern from the create form itself, so route through
    // the same list-page toggle the manage page uses.
    setBusy(true)
    try {
      await patchDestination(createdID, { enabled: true })
      toast.success('Destination added')
      navigate('/settings/destinations')
    } catch (err) {
      toast.error('Could not enable destination', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  const tested = test?.ok === true
  const testState: 'gate' | 'testing' | 'ok' | 'failed' = busy
    ? 'testing'
    : tested
      ? 'ok'
      : test && !test.ok
        ? 'failed'
        : 'gate'

  const destinationTitle = `Add ${kind === 'email' ? 'Email' : kind === 'channel' ? 'Channel' : isRepoKind ? gitKindMeta(kind ?? '').label : 'Webhook'} destination`

  return (
    <PageShell width="form">
      <PageHeader
        title={destinationTitle}
        description={`kind: ${kind}`}
        breadcrumbs={[
          { label: 'Settings', href: '/settings' },
          { label: area.label, href: '/settings/destinations' },
          { label: destinationTitle },
        ]}
      />

      <Form onSubmit={(e) => e.preventDefault()}>
        <FieldGroup>
          <Field label="Name" description="unique">
            <Input
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                invalidate()
              }}
              placeholder="ops-inbox"
            />
          </Field>

          <DestinationKindFields
            kind={kind}
            values={values}
            setField={setField}
            connectors={connectors ?? []}
            channels={channels ?? []}
          />
          {kind === 'channel' && channels && channels.length === 0 && (
            <p className="-mt-2 text-sm text-muted-foreground">No channels yet, add one under Channels first.</p>
          )}
          {kind === 'email' && connectors && connectors.length === 0 && (
            <p className="-mt-2 text-sm text-muted-foreground">
              No enabled Google connectors yet - add one under Connectors first.
            </p>
          )}
          {isRepoKind && connectors && connectors.length === 0 && (
            <p className="-mt-2 text-sm text-muted-foreground">
              No enabled {gitKindMeta(kind ?? '').label} connectors yet, add one under Connectors first.
            </p>
          )}

        </FieldGroup>

        {!isRepoKind &&
          (testState === 'gate' ? (
            <div className="flex flex-wrap items-center gap-3 rounded-md border border-border bg-muted/40 p-4 text-sm text-muted-foreground">
              <span className="min-w-0 flex-1 font-medium">Not tested yet, run a test before adding.</span>
              <Button size="sm" variant="test" disabled={busy || !canTest} onClick={() => void runTest()}>
                Test send
              </Button>
            </div>
          ) : (
            <TestStatus
              state={testState as 'testing' | 'ok' | 'failed'}
              message={
                testState === 'testing'
                  ? 'Testing…'
                  : testState === 'ok'
                    ? kind === 'channel'
                      ? 'Channel reachable, ready to add.'
                      : 'Test delivery sent, ready to add.'
                    : `Test failed: ${test?.error}. The destination was saved disabled, fix and retry.`
              }
              action={
                testState !== 'testing' && (
                  <Button size="sm" variant="test" disabled={busy || !canTest} onClick={() => void runTest()}>
                    Test send
                  </Button>
                )
              }
            />
          ))}

        <FormActions>
          <Button type="button" variant="outline" disabled={busy} onClick={() => navigate('/settings/destinations')}>
            Cancel
          </Button>
          {isRepoKind ? (
            <Button disabled={!canTest || busy} onClick={() => void submitGitHub()}>
              {busy ? 'Adding…' : 'Add destination'}
            </Button>
          ) : (
            <Button disabled={!tested || busy} onClick={() => void submit()}>
              Add destination
            </Button>
          )}
        </FormActions>
      </Form>
    </PageShell>
  )
}
