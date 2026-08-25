import { ArrowLeft01Icon, Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router'
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
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { Input } from '../ui/input'
import { ConnectorLogo } from './ConnectorLogo'
import { connectorPresets, unknownPreset } from './connectorPresets'
import { Field, Toggle } from './shared'
import { connectedAs, errText, isTimothyAuthError } from './util'

// oauthProviderLabel names the OAuth provider for a connector kind —
// both google and microsoft share the same reconnect/test UI shape.
function oauthProviderLabel(kind: string): string {
  return kind === 'microsoft' ? 'Microsoft' : 'Google'
}

export function ConnectorEdit() {
  const { id } = useParams()
  const navigate = useNavigate()

  const [connector, setConnector] = useState<AdminConnector | null | undefined>(undefined)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [test, setTest] = useState<{ ok: boolean; error?: string; identity?: GitHubIdentity } | null>(
    null,
  )
  const [testing, setTesting] = useState(false)
  const [token, setToken] = useState('')
  const [savingToken, setSavingToken] = useState(false)
  const [oauthBusy, setOAuthBusy] = useState(false)

  const refresh = useCallback(() => {
    listConnectors()
      .then((list) => setConnector(list.find((c) => c.id === id) ?? null))
      .catch((err: unknown) => toast.error('Could not load connector', { description: errText(err) }))
  }, [id])
  useEffect(refresh, [refresh])

  const remove = async () => {
    if (!connector) return
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
    if (!connector) return
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
    if (!connector || !token) return
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
      refresh()
    } catch (err) {
      toast.error('Could not save token', { description: errText(err) })
    } finally {
      setSavingToken(false)
    }
  }

  const toggleSensitive = (sensitive: boolean) => {
    if (!connector) return
    patchConnector(connector.id, { sensitive })
      .then(refresh)
      .catch((err: unknown) => toast.error('Could not update connector', { description: errText(err) }))
  }

  const [signingBusy, setSigningBusy] = useState(false)
  const toggleSignCommits = async (sign_commits: boolean) => {
    if (!connector) return
    setSigningBusy(true)
    try {
      await patchConnector(connector.id, { config: { ...connector.config, sign_commits } })
      refresh()
    } catch (err) {
      toast.error('Could not update connector', { description: errText(err) })
    } finally {
      setSigningBusy(false)
    }
  }
  const copyPublicKey = async () => {
    const key = connector?.config.signing_public_key
    if (typeof key !== 'string') return
    await navigator.clipboard.writeText(key)
    toast.success('Public key copied')
  }

  const reconnectOAuth = async () => {
    if (!connector) return
    setOAuthBusy(true)
    try {
      window.location.assign(await connectorOAuthStart(connector.id))
    } catch (err) {
      toast.error(`Could not start ${oauthProviderLabel(connector.kind)} re-connect`, { description: errText(err) })
      setOAuthBusy(false)
    }
  }

  if (connector === null) return <Navigate to="/settings/connectors" replace />
  if (connector === undefined) return null

  const preset = connectorPresets.find((p) => p.kind === connector.kind) ?? unknownPreset
  const isOAuth = connector.kind === 'google' || connector.kind === 'microsoft'

  return (
    <div className="mt-6 w-full space-y-6">
      <Link
        to="/settings/connectors"
        className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground transition hover:text-foreground"
      >
        <HugeiconsIcon icon={ArrowLeft01Icon} className="size-4" />
        Connectors
      </Link>

      <div className="flex items-center gap-4 border-b border-border pb-6">
        <ConnectorLogo preset={preset} className="size-12" />
        <div className="min-w-0 flex-1">
          <h1 className="truncate text-xl font-semibold tracking-tight">{connector.name}</h1>
          <p className="text-sm text-muted-foreground uppercase">{connector.kind}</p>
        </div>
        <Button variant="destructive" onClick={() => setConfirmDelete(true)}>
          <HugeiconsIcon icon={Delete02Icon} />
          Delete
        </Button>
      </div>

      <div className="grid max-w-3xl gap-5">
        <div className="flex items-center justify-between gap-4 rounded-xl border border-border p-4">
          <div className="min-w-0">
            <div className="text-sm font-medium">Treat as sensitive</div>
            <p className="text-sm text-muted-foreground">
              Pins related turns to the privacy-floor route, keeping this connector's
              data off third-party models.
            </p>
          </div>
          <Toggle
            on={connector.sensitive}
            onChange={toggleSensitive}
            label={`${connector.name} sensitive`}
          />
        </div>

        <h2 className="text-sm font-semibold">Connection</h2>

        <div
          className={
            'flex flex-wrap items-center gap-3 rounded-xl border p-4 text-sm ' +
            (test?.ok
              ? 'border-good/30 bg-good-soft text-good'
              : test && !test.ok
                ? 'border-destructive/30 bg-destructive/5 text-destructive'
                : 'border-border bg-muted/40 text-muted-foreground')
          }
        >
          <span className="min-w-0 flex-1 font-medium">
            {testing
              ? 'Testing connection…'
              : test?.ok
                ? test.identity
                  ? `${connectedAs(test.identity)}, ${test.identity.scopes}.`
                  : 'Connection OK, tools are servable.'
                : test && !test.ok
                  ? `Failed: ${test.error}`
                  : 'Not tested yet.'}
          </span>
          {test && !test.ok && isOAuth ? (
            <Button size="sm" variant="outline" disabled={oauthBusy} onClick={() => void reconnectOAuth()}>
              {oauthBusy ? 'Redirecting…' : 'Reconnect'}
            </Button>
          ) : (
            <Button size="sm" variant="test" disabled={testing} onClick={() => void runTest()}>
              {testing ? 'Testing…' : 'Test connection'}
            </Button>
          )}
        </div>

        {test && !test.ok && connector.kind === 'github' && (
          <p className="-mt-3 text-sm text-muted-foreground">
            Paste a new personal access token below to replace it.
          </p>
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
                'Identity for mission clone/push/PR use, no chat tools.'
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
            >
              <div className="mt-1.5 flex gap-2">
                <Input
                  type="password"
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  placeholder="paste new token"
                  className="h-10"
                  autoComplete="off"
                />
                <Button variant="outline" disabled={savingToken || !token} onClick={() => void rotateToken()}>
                  Save
                </Button>
              </div>
            </Field>
          </div>
        )}

        {connector.kind === 'github' && (
          <div className="space-y-3 border-t border-border pt-5">
            <div className="flex items-center justify-between gap-4">
              <div className="min-w-0">
                <div className="text-sm font-medium">Sign commits</div>
                <p className="text-sm text-muted-foreground">
                  SSH-sign every mission commit made through this connector with a key Timothy
                  generates, so they show "Verified" on GitHub.
                </p>
              </div>
              <Toggle
                on={Boolean(connector.config.sign_commits)}
                onChange={(v) => void toggleSignCommits(v)}
                label={`${connector.name} sign commits`}
              />
            </div>
            {Boolean(connector.config.sign_commits) && (
              <div className="space-y-2">
                {typeof connector.config.signing_public_key === 'string' &&
                connector.config.signing_public_key ? (
                  <>
                    <Field label="Signing public key">
                      <div className="mt-1.5 flex gap-2">
                        <textarea
                          readOnly
                          value={connector.config.signing_public_key}
                          rows={3}
                          className="h-auto flex-1 resize-none rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs"
                        />
                        <Button variant="outline" onClick={() => void copyPublicKey()}>
                          Copy
                        </Button>
                      </div>
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
                  <p className="text-sm text-muted-foreground">
                    {signingBusy ? 'Generating key…' : 'No public key yet.'}
                  </p>
                )}
              </div>
            )}
          </div>
        )}
      </div>

      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {connector.name}?</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            Removes the connector; its tools disappear from the agent on the next reload. Stored
            credentials stay in the secret store until cleared there.
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
