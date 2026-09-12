import { Trash2 } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import {
  connectorOAuthStart,
  deleteConnector,
  listConnectors,
  patchConnector,
  setSecret,
  testConnector,
} from '../../api/client'
import type { AdminConnector, GitHubIdentity } from '../../api/types'
import { Button } from '../ui/button'
import { Switch } from '../ui/switch'
import { Alert, AlertDescription } from '../ui/alert'
import { ConfirmDialog } from '../timothy/confirm-dialog'
import { Field, FieldGroup, Form, FormActions } from '../timothy/field'
import { Panel } from '../timothy/panel'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { Input } from '../ui/input'
import { ConnectorLogo } from './ConnectorLogo'
import { presetFor } from './connectorPresets'
import { settingsArea } from './settingsAreas'
import { TestStatus } from './TestStatus'
import { useStagedForm } from './useStagedForm'
import { connectedAs } from './util'
import { errText, isTimothyAuthError } from '../../lib/errors'
import { slugify } from '../../lib/slugify'

const area = settingsArea('connectors')

// oauthProviderLabel names the OAuth provider for a connector kind:
// both google and microsoft share the same reconnect/test UI shape.
function oauthProviderLabel(kind: string): string {
  return kind === 'microsoft' ? 'Microsoft' : 'Google'
}

interface StagedConnector {
  name: string
  sensitive: boolean
  sign_commits: boolean
}

function baselineFrom(connector: AdminConnector): StagedConnector {
  return {
    name: connector.name,
    sensitive: connector.sensitive,
    sign_commits: Boolean(connector.config.sign_commits),
  }
}

// buildPatch builds the single PATCH body from the staged values: name
// (slugified), sensitive, and config.sign_commits merged onto the
// connector's current config so other config keys survive.
function buildPatch(connector: AdminConnector, staged: StagedConnector): Partial<AdminConnector> {
  return {
    name: slugify(staged.name),
    sensitive: staged.sensitive,
    config: { ...connector.config, sign_commits: staged.sign_commits },
  }
}

// ConnectorEdit loads the connector, then hands off to ConnectorEditForm
// keyed by its id: a fresh mount per connector so useStagedForm's
// baseline is never initialized from a placeholder before the real
// data arrives.
export function ConnectorEdit() {
  const { id } = useParams()
  const [connector, setConnector] = useState<AdminConnector | null | undefined>(undefined)

  const refresh = useCallback(() => {
    return listConnectors()
      .then((list) => {
        const found = list.find((c) => c.id === id) ?? null
        setConnector(found)
        return found
      })
      .catch((err: unknown) => {
        toast.error('Could not load connector', { description: errText(err) })
        return undefined
      })
  }, [id])
  useEffect(() => {
    void refresh()
  }, [refresh])

  if (connector === null) return <Navigate to="/settings/connectors" replace />
  if (connector === undefined) return null

  return <ConnectorEditForm key={connector.id} initialConnector={connector} refresh={refresh} />
}

function ConnectorEditForm({
  initialConnector,
  refresh,
}: {
  initialConnector: AdminConnector
  refresh: () => Promise<AdminConnector | null | undefined>
}) {
  const navigate = useNavigate()

  const [connector, setConnectorState] = useState(initialConnector)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [test, setTest] = useState<{ ok: boolean; error?: string; identity?: GitHubIdentity } | null>(
    null,
  )
  const [testing, setTesting] = useState(false)
  const [token, setToken] = useState('')
  const [savingToken, setSavingToken] = useState(false)
  const [oauthBusy, setOAuthBusy] = useState(false)

  const staged = useStagedForm<StagedConnector>(baselineFrom(initialConnector))

  const doRefresh = useCallback(async () => {
    const refetched = await refresh()
    if (refetched) setConnectorState(refetched)
    return refetched
  }, [refresh])

  const save = useCallback(async () => {
    setSaving(true)
    setSaveError(null)
    try {
      const patch = buildPatch(connector, staged.values)
      await patchConnector(connector.id, patch)
      toast.success('Connector saved')
      const refetched = await doRefresh()
      if (refetched) staged.rebase(baselineFrom(refetched))
    } catch (err) {
      setSaveError(errText(err))
    } finally {
      setSaving(false)
    }
  }, [connector, doRefresh, staged])

  const remove = async () => {
    try {
      await deleteConnector(connector.id)
      toast.success('Connector removed', { description: `${connector.name}'s tools are no longer available.` })
      navigate('/settings/connectors')
    } catch (err) {
      toast.error('Could not remove connector', { description: errText(err) })
      setConfirmDelete(false)
    }
  }

  const runTest = async () => {
    setTesting(true)
    setTest(null)
    try {
      setTest(await testConnector(connector.id))
    } catch (err) {
      if (isTimothyAuthError(err)) {
        setTest(null)
        return
      }
      setTest({ ok: false, error: errText(err) })
    } finally {
      setTesting(false)
    }
  }

  const rotateToken = async () => {
    if (!token) return
    setSavingToken(true)
    try {
      const suffix =
        connector.kind === 'github'
          ? '_GITHUB_PAT'
          : connector.kind === 'imap'
            ? '_IMAP_PASSWORD'
            : connector.kind === 'caldav'
              ? '_CALDAV_PASSWORD'
              : '_MCP_TOKEN'
      const ref = connector.credential_ref || `${connector.name.toUpperCase().replace(/-/g, '_')}${suffix}`
      await setSecret(ref, token.trim())
      if (!connector.credential_ref) await patchConnector(connector.id, { credential_ref: ref })
      setToken('')
      toast.success('Token saved')
      void doRefresh()
    } catch (err) {
      toast.error('Could not save token', { description: errText(err) })
    } finally {
      setSavingToken(false)
    }
  }

  const copyPublicKey = async () => {
    const key = connector.config.signing_public_key
    if (typeof key !== 'string') return
    await navigator.clipboard.writeText(key)
    toast.success('Public key copied')
  }

  const reconnectOAuth = async () => {
    setOAuthBusy(true)
    try {
      window.location.assign(await connectorOAuthStart(connector.id))
    } catch (err) {
      toast.error(`Could not start ${oauthProviderLabel(connector.kind)} re-connect`, { description: errText(err) })
      setOAuthBusy(false)
    }
  }

  const preset = presetFor(connector)
  const isOAuth = connector.kind === 'google' || connector.kind === 'microsoft'

  return (
    <PageShell width="form">
      <PageHeader
        title={connector.name}
        description={`kind: ${connector.kind}`}
        meta={<ConnectorLogo preset={preset} className="size-9" />}
        breadcrumbs={[
          { label: 'Settings', href: '/settings' },
          { label: area.label, href: '/settings/connectors' },
          { label: connector.name },
        ]}
        actions={
          <Button variant="destructive" onClick={() => setConfirmDelete(true)}>
            <Trash2 />
            Delete
          </Button>
        }
      />

      <Form
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <FieldGroup>
          <Field label="Connector name">
            <Input value={staged.values.name} onChange={(e) => staged.setField('name', e.target.value)} />
          </Field>

          <Field label="Treat as sensitive" required={false}>
            {() => (
              <div className="flex items-center gap-3 text-sm">
                <Switch
                  checked={staged.values.sensitive}
                  onCheckedChange={(v) => staged.setField('sensitive', v)}
                  aria-label={`${connector.name} sensitive`}
                />
                <span className="text-muted-foreground">
                  Pins related turns to the privacy-floor route, keeping this connector's data off
                  third-party models.
                </span>
              </div>
            )}
          </Field>

          {connector.kind === 'github' && (
            <Field label="Sign commits" required={false}>
              {() => (
                <div className="flex items-center gap-3 text-sm">
                  <Switch
                    checked={staged.values.sign_commits}
                    onCheckedChange={(v) => staged.setField('sign_commits', v)}
                    aria-label={`${connector.name} sign commits`}
                  />
                  <span className="text-muted-foreground">
                    SSH-sign every mission commit made through this connector with a key Timothy
                    generates, so they show "Verified" on GitHub.
                  </span>
                </div>
              )}
            </Field>
          )}
        </FieldGroup>

        {connector.kind === 'github' && staged.values.sign_commits && (
          <div className="space-y-2">
            {typeof connector.config.signing_public_key === 'string' && connector.config.signing_public_key ? (
              <>
                <Field label="Signing public key">
                  {(props) => (
                    <div className="flex gap-2">
                      <textarea
                        id={props.id}
                        readOnly
                        value={connector.config.signing_public_key as string}
                        rows={3}
                        className="h-auto flex-1 resize-none rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs"
                      />
                      <Button type="button" variant="outline" onClick={() => void copyPublicKey()}>
                        Copy
                      </Button>
                    </div>
                  )}
                </Field>
                <p className="text-sm text-muted-foreground">
                  Paste this into GitHub as a{' '}
                  <a
                    href="https://github.com/settings/ssh/new"
                    target="_blank"
                    rel="noreferrer"
                    className="font-medium text-primary underline underline-offset-2 hover:no-underline"
                  >
                    new SSH key →
                  </a>{' '}
                  with key type <span className="font-medium">Signing Key</span>.
                </p>
              </>
            ) : (
              <p className="text-sm text-muted-foreground">A signing key is generated when you save.</p>
            )}
          </div>
        )}

        {saveError && (
          <Alert tone="destructive">
            <AlertDescription className="flex flex-wrap items-center justify-between gap-3">
              <span>Could not save connector changes: {saveError}</span>
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

      <div className="mt-10">
        <Panel title="Connection">
          <div className="space-y-3">
            {test || testing ? (
              <TestStatus
                state={testing ? 'testing' : test?.ok ? 'ok' : 'failed'}
                message={
                  testing
                    ? 'Testing connection…'
                    : test?.ok
                      ? test.identity
                        ? `${connectedAs(test.identity)}, ${test.identity.scopes}.`
                        : 'Connection OK, tools are servable.'
                      : `Failed: ${test?.error}`
                }
                action={
                  test && !test.ok && isOAuth ? (
                    <Button size="sm" variant="outline" disabled={oauthBusy} onClick={() => void reconnectOAuth()}>
                      {oauthBusy ? 'Redirecting…' : 'Reconnect'}
                    </Button>
                  ) : (
                    !testing && (
                      <Button size="sm" variant="test" onClick={() => void runTest()}>
                        Test connection
                      </Button>
                    )
                  )
                }
              />
            ) : (
              <div className="flex flex-wrap items-center gap-3 rounded-md border border-border bg-muted/40 p-4 text-sm text-muted-foreground">
                <span className="min-w-0 flex-1 font-medium">Not tested yet.</span>
                <Button size="sm" variant="test" onClick={() => void runTest()}>
                  Test connection
                </Button>
              </div>
            )}

            {test && !test.ok && connector.kind === 'github' && (
              <p className="text-sm text-muted-foreground">Paste a new personal access token below to replace it.</p>
            )}

            {isOAuth ? (
              <div className="space-y-3">
                <p className="text-sm text-muted-foreground">
                  Scopes: {(connector.config.scopes as string[] | undefined)?.map((s) => s.split('/').pop()).join(', ')}
                </p>
                <Button variant="outline" disabled={oauthBusy} onClick={() => void reconnectOAuth()}>
                  {oauthBusy ? 'Redirecting…' : `Reconnect ${oauthProviderLabel(connector.kind)} account`}
                </Button>
              </div>
            ) : (
              <div className="space-y-3">
                <p className="text-sm text-muted-foreground">
                  {connector.kind === 'github' ? (
                    'Identity for mission clone/push/PR use, plus read-only pull request tools.'
                  ) : connector.kind === 'imap' ? (
                    <>
                      <span className="font-mono">
                        {String(connector.config.username ?? '')} @ {String(connector.config.host ?? '')}
                      </span>
                      {typeof connector.config.smtp_host === 'string' && connector.config.smtp_host && (
                        <>
                          {' '}
                          · SMTP: <span className="font-mono">{connector.config.smtp_host}</span>
                        </>
                      )}
                    </>
                  ) : connector.kind === 'caldav' ? (
                    <span className="font-mono">
                      {String(connector.config.username ?? '')} @ {String(connector.config.url ?? '')}
                    </span>
                  ) : (
                    <>
                      Endpoint: <span className="font-mono">{String(connector.config.endpoint ?? '')}</span>
                    </>
                  )}
                </p>
                <Field
                  label={
                    connector.kind === 'github'
                      ? 'Rotate personal access token'
                      : connector.kind === 'imap' || connector.kind === 'caldav'
                        ? 'Rotate password'
                        : 'Rotate bearer token'
                  }
                  required={false}
                >
                  {(props) => (
                    <div className="flex gap-2">
                      <Input
                        id={props.id}
                        type="password"
                        value={token}
                        onChange={(e) => setToken(e.target.value)}
                        placeholder="paste new token"
                        autoComplete="off"
                      />
                      <Button variant="outline" disabled={savingToken || !token} onClick={() => void rotateToken()}>
                        Save
                      </Button>
                    </div>
                  )}
                </Field>
              </div>
            )}
          </div>
        </Panel>
      </div>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete ${connector.name}?`}
        description="Removes the connector; its tools disappear from the agent on the next reload. Stored credentials stay in the secret store until cleared there."
        confirmLabel="Delete"
        destructive
        onConfirm={() => void remove()}
      />
    </PageShell>
  )
}
