import { useEffect, useState } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { createDestination, listConnectors, patchDestination, setSecret, testDestination } from '../../api/client'
import type { AdminConnector } from '../../api/types'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { Field, FieldGroup, Form, FormActions } from '../timothy/field'
import { CredentialField, type CredentialMode } from './CredentialRefPicker'
import { DestinationKindFields, type DestinationKindValues } from './DestinationKindFields'
import { settingsArea } from './settingsAreas'
import { TestStatus } from './TestStatus'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'
import { errText } from '../../lib/errors'
import { slugify } from '../../lib/slugify'

const area = settingsArea('destinations')

// DestinationAdd is kind-aware (email vs webhook vs telegram vs github)
// and its own page, mirroring ConnectorAdd's shape: the destination row
// is created (disabled) as part of testing, and a passing test enables
// it. GitHub has no test-send affordance, so it creates enabled
// directly.
export function DestinationAdd() {
  const { kind } = useParams<{ kind: 'email' | 'webhook' | 'telegram' | 'github' }>()
  const navigate = useNavigate()
  const defaultBackend = useDefaultSecretBackend()

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
    mode: 'push',
    branchPattern: '',
    commitStyle: '',
    createIfMissing: false,
  })
  const setField = <K extends keyof DestinationKindValues>(key: K, value: DestinationKindValues[K]) => {
    setValues((prev) => ({ ...prev, [key]: value }))
    invalidate()
  }

  // telegram bot token
  const [botToken, setBotToken] = useState('')
  const [botTokenMode, setBotTokenMode] = useState<CredentialMode>('new')
  const [existingBotTokenRef, setExistingBotTokenRef] = useState('')

  useEffect(() => {
    if (kind !== 'email' && kind !== 'github') return
    const wantKind = kind === 'email' ? 'google' : 'github'
    listConnectors()
      .then((rows) => setConnectors(rows.filter((c) => c.kind === wantKind && c.enabled)))
      .catch((err: unknown) => toast.error('Could not load connectors', { description: errText(err) }))
  }, [kind])

  if (kind !== 'email' && kind !== 'webhook' && kind !== 'telegram' && kind !== 'github') {
    return <Navigate to="/settings/destinations" replace />
  }

  function invalidate() {
    setTest(null)
    setCreatedID(null)
  }

  const slug = slugify(name)
  const usingExistingBotToken = botTokenMode === 'existing'
  const botTokenRef = usingExistingBotToken ? existingBotTokenRef : `${slug.toUpperCase().replace(/-/g, '_')}_TELEGRAM_BOT_TOKEN`

  const config =
    kind === 'email'
      ? { connector_id: values.connectorID, to: values.to.trim() }
      : kind === 'telegram'
        ? { chat_id: values.chatID.trim() }
        : kind === 'github'
          ? {
              connector_id: values.connectorID,
              mode: values.mode,
              branch_pattern: values.branchPattern.trim() || undefined,
              commit_style: values.commitStyle || undefined,
              create_if_missing: values.createIfMissing || undefined,
            }
          : { url: values.url.trim(), format: values.format }

  const canTest =
    slug !== '' &&
    (kind === 'email'
      ? values.connectorID !== '' && values.to.trim() !== ''
      : kind === 'telegram'
        ? values.chatID.trim() !== '' && (usingExistingBotToken ? existingBotTokenRef !== '' : botToken.trim() !== '')
        : kind === 'github'
          ? values.connectorID !== ''
          : values.url.trim() !== '')

  const runTest = async () => {
    setBusy(true)
    setTest(null)
    try {
      if (kind === 'telegram' && !usingExistingBotToken) await setSecret(botTokenRef, botToken.trim())
      const id = await createDestination({
        name: slug,
        kind,
        config,
        credential_ref: kind === 'telegram' ? botTokenRef : undefined,
        enabled: false,
      })
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
      await createDestination({ name: slug, kind: 'github', config, enabled: true })
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

  const destinationTitle = `Add ${kind === 'email' ? 'Email' : kind === 'telegram' ? 'Telegram' : kind === 'github' ? 'GitHub' : 'Webhook'} destination`

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
          <Field label="Name" description="lowercase slug">
            <Input
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                invalidate()
              }}
              placeholder="ops-inbox"
            />
          </Field>

          <DestinationKindFields kind={kind} values={values} setField={setField} connectors={connectors ?? []} />
          {kind === 'email' && connectors && connectors.length === 0 && (
            <p className="-mt-2 text-sm text-muted-foreground">
              No enabled Google connectors yet - add one under Connectors first.
            </p>
          )}
          {kind === 'github' && connectors && connectors.length === 0 && (
            <p className="-mt-2 text-sm text-muted-foreground">
              No enabled GitHub connectors yet, add one under Connectors first.
            </p>
          )}

          {kind === 'telegram' && (
            <CredentialField
              label="Bot token"
              mode={botTokenMode}
              onModeChange={(m) => {
                setBotTokenMode(m)
                invalidate()
              }}
              existingRef={existingBotTokenRef}
              onExistingRefChange={(v) => {
                setExistingBotTokenRef(v)
                invalidate()
              }}
              secretValue={botToken}
              onSecretValueChange={(v) => {
                setBotToken(v)
                invalidate()
              }}
              secretPlaceholder="123456:ABC-DEF..."
              defaultBackend={defaultBackend}
              refName={botTokenRef}
            />
          )}
        </FieldGroup>

        {kind !== 'github' &&
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
                    ? 'Test delivery sent, ready to add.'
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
          {kind === 'github' ? (
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
