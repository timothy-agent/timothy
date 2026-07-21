import { Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router'
import {
  connectorOAuthStart,
  createConnector,
  deleteConnector,
  listConnectors,
  patchConnector,
  setSecret,
  testConnector,
} from '../../api/client'
import type { AdminConnector } from '../../api/types'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { Input } from '../ui/input'
import { ConnectorLogo, ConnectorLogoSprite } from './ConnectorLogo'
import { connectorPresets, type ConnectorPreset } from './connectorPresets'
import { ErrorBanner, Field, Toggle } from './shared'
import { errText } from './util'

// slugify turns a display name into a connector name (tool-name
// prefix): lowercase slug, the backend rejects anything else.
function slugify(v: string): string {
  return v
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

// matchConnectorPreset finds the preset a configured connector came
// from, for card branding.
function matchConnectorPreset(c: AdminConnector): ConnectorPreset {
  const custom = connectorPresets.find((p) => p.id === 'custom-mcp')!
  if (c.kind === 'google') {
    const scopes = JSON.stringify(c.config.scopes ?? '')
    return (
      connectorPresets.find((p) => p.kind === 'google' && p.scopes?.every((s) => scopes.includes(s))) ?? custom
    )
  }
  const endpoint = String(c.config.endpoint ?? '')
  return (
    connectorPresets.find((p) => p.kind === 'mcp' && p.endpoint && endpoint.startsWith(p.endpoint)) ?? custom
  )
}

export function ConnectorsTab() {
  const [connectors, setConnectors] = useState<AdminConnector[]>([])
  const [error, setError] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<AdminConnector | null>(null)
  const [connecting, setConnecting] = useState<ConnectorPreset | null>(null)
  const [params, setParams] = useSearchParams()

  // The OAuth callback bounces back here with the outcome in the query.
  const oauthConnected = params.get('oauth_connected')
  const oauthError = params.get('oauth_error')
  const clearOAuthParams = () => {
    setParams({ tab: 'connectors' }, { replace: true })
  }

  const refresh = useCallback(() => {
    listConnectors()
      .then((list) => {
        setConnectors(list)
        setError(null)
      })
      .catch((err: unknown) => setError(errText(err)))
  }, [])
  useEffect(refresh, [refresh])

  const remove = async () => {
    if (!confirmDelete) return
    try {
      await deleteConnector(confirmDelete.id)
      setConfirmDelete(null)
      refresh()
    } catch (err) {
      setError(errText(err))
      setConfirmDelete(null)
    }
  }

  return (
    <div className="mt-6 space-y-4">
      <ConnectorLogoSprite />
      <p className="text-sm text-muted-foreground">
        Integrations the agent can use as tools — each connector&apos;s tools appear to the
        model as <span className="font-mono text-xs">name_tool</span> and go through the same
        permission prompts as everything else.
      </p>
      <ErrorBanner message={error} />
      {oauthConnected && (
        <div className="flex items-center gap-3 rounded-xl border border-emerald-500/30 bg-emerald-500/5 p-3 text-sm text-emerald-700 dark:text-emerald-400">
          <span>Google account connected to “{oauthConnected}”. Enable it below to serve tools.</span>
          <button type="button" onClick={clearOAuthParams} className="ml-auto text-xs underline-offset-2 hover:underline">
            dismiss
          </button>
        </div>
      )}
      {oauthError && (
        <div className="flex items-center gap-3 rounded-xl border border-red-500/30 bg-red-500/5 p-3 text-sm text-red-600 dark:text-red-400">
          <span>Google connection failed: {oauthError}</span>
          <button type="button" onClick={clearOAuthParams} className="ml-auto text-xs underline-offset-2 hover:underline">
            dismiss
          </button>
        </div>
      )}

      {connectors.length > 0 && (
        <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Your connectors · {connectors.length}
        </h2>
      )}
      {connectors.map((c) => (
        <ConnectorCard
          key={c.id}
          connector={c}
          onChanged={refresh}
          onDelete={() => setConfirmDelete(c)}
          onError={setError}
        />
      ))}
      {connectors.length === 0 && !error && (
        <div className="rounded-xl border border-dashed p-10 text-center text-sm text-muted-foreground">
          No connectors yet — add one below.
        </div>
      )}

      <h2 className="pt-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Add a connector
      </h2>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {connectorPresets.map((preset) => (
          <button
            key={preset.id}
            type="button"
            onClick={() => setConnecting(preset)}
            className="flex items-center gap-3 rounded-xl border border-border p-3 text-left transition hover:border-zinc-400 hover:bg-zinc-50 dark:hover:border-zinc-600 dark:hover:bg-zinc-900"
          >
            <ConnectorLogo preset={preset} className="size-8" />
            <span className="min-w-0">
              <span className="block text-sm font-medium">{preset.name}</span>
              <span className="block truncate text-xs text-muted-foreground">
                {preset.description}
              </span>
            </span>
            <span className="ml-auto text-muted-foreground" aria-hidden="true">
              +
            </span>
          </button>
        ))}
      </div>

      <AddConnectorDialog
        preset={connecting}
        onClose={() => setConnecting(null)}
        onAdded={() => {
          setConnecting(null)
          refresh()
        }}
        onRefresh={refresh}
        onError={setError}
      />

      <Dialog open={confirmDelete !== null} onOpenChange={(o) => !o && setConfirmDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {confirmDelete?.name}?</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            Removes the connector; its tools disappear from the agent on the next reload.
            Stored credentials stay in the secret store until cleared there.
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

function ConnectorCard({
  connector,
  onChanged,
  onDelete,
  onError,
}: {
  connector: AdminConnector
  onChanged: () => void
  onDelete: () => void
  onError: (msg: string) => void
}) {
  const preset = matchConnectorPreset(connector)
  const [testing, setTesting] = useState(false)
  const [test, setTest] = useState<{ ok: boolean; error?: string } | null>(null)
  const [oauthBusy, setOAuthBusy] = useState(false)

  const toggle = (enabled: boolean) => {
    patchConnector(connector.id, { enabled }).then(onChanged, (err: unknown) =>
      onError(errText(err)),
    )
  }

  const runTest = async () => {
    setTesting(true)
    setTest(null)
    try {
      setTest(await testConnector(connector.id))
    } catch (err) {
      setTest({ ok: false, error: errText(err) })
    } finally {
      setTesting(false)
    }
  }

  const startOAuth = async () => {
    setOAuthBusy(true)
    try {
      window.location.assign(await connectorOAuthStart(connector.id))
    } catch (err) {
      onError(errText(err))
      setOAuthBusy(false)
    }
  }

  return (
    <div className="rounded-xl border border-border p-4">
      <div className="flex flex-wrap items-center gap-3">
        <ConnectorLogo preset={preset} />
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium">{connector.name}</span>
            <span className="rounded border border-border px-1.5 py-px text-[10px] font-medium uppercase text-muted-foreground">
              {connector.kind}
            </span>
          </div>
          <div className="text-xs text-muted-foreground">
            {connector.kind === 'mcp'
              ? String(connector.config.endpoint ?? '')
              : (connector.config.scopes as string[] | undefined)?.map((s) => s.split('/').pop()).join(', ')}
          </div>
        </div>
        <div className="ml-auto flex items-center gap-3">
          {connector.kind === 'google' && (
            <Button size="sm" variant="outline" disabled={oauthBusy} onClick={() => void startOAuth()}>
              {oauthBusy ? 'Redirecting…' : 'Connect Google account'}
            </Button>
          )}
          <Button size="sm" variant="outline" disabled={testing} onClick={() => void runTest()}>
            {testing ? 'Testing…' : 'Test'}
          </Button>
          <Toggle on={connector.enabled} onChange={toggle} label={`${connector.name} enabled`} />
          <button
            type="button"
            aria-label={`Delete ${connector.name}`}
            onClick={onDelete}
            className="text-muted-foreground hover:text-red-500"
          >
            <HugeiconsIcon icon={Delete02Icon} className="size-4" />
          </button>
        </div>
      </div>
      {test && (
        <div
          className={`mt-3 rounded-lg border p-2.5 text-xs ${test.ok ? 'border-emerald-500/30 bg-emerald-500/5 text-emerald-700 dark:text-emerald-400' : 'border-red-500/30 bg-red-500/5 text-red-600 dark:text-red-400'}`}
        >
          {test.ok ? 'Connection OK — tools are servable.' : `Failed: ${test.error}`}
        </div>
      )}
    </div>
  )
}

// AddConnectorDialog is preset-aware: MCP presets take an endpoint +
// token and are created-tested-enabled in one go; Google presets take
// the OAuth client and hand off to Google's consent screen.
function AddConnectorDialog({
  preset,
  onClose,
  onAdded,
  onRefresh,
  onError,
}: {
  preset: ConnectorPreset | null
  onClose: () => void
  onAdded: () => void
  onRefresh: () => void
  onError: (msg: string) => void
}) {
  const [name, setName] = useState('')
  const [endpoint, setEndpoint] = useState('')
  const [token, setToken] = useState('')
  const [clientID, setClientID] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState<string | null>(null)

  useEffect(() => {
    if (!preset) return
    setName(slugify(preset.id === 'custom-mcp' ? '' : preset.name))
    setEndpoint(preset.endpoint ?? '')
    setToken('')
    setClientID('')
    setClientSecret('')
    setBusy(false)
    setStatus(null)
  }, [preset])

  if (!preset) return null
  const isGoogle = preset.kind === 'google'
  const slug = slugify(name)
  const canSubmit =
    !busy &&
    slug !== '' &&
    (isGoogle ? clientID.trim() !== '' && clientSecret !== '' : endpoint.trim() !== '')

  const refBase = slug.toUpperCase().replace(/-/g, '_')

  const submit = async () => {
    setBusy(true)
    setStatus(null)
    try {
      if (isGoogle) {
        const secretRef = `${refBase}_GOOGLE_CLIENT_SECRET`
        await setSecret(secretRef, clientSecret)
        const id = await createConnector({
          name: slug,
          kind: 'google',
          config: {
            client_id: clientID.trim(),
            client_secret_ref: secretRef,
            scopes: preset.scopes,
          },
          credential_ref: `${refBase}_GOOGLE_OAUTH`,
          enabled: false,
        })
        setStatus('Redirecting to Google…')
        window.location.assign(await connectorOAuthStart(id))
        return // navigation takes over
      }

      const tokenRef = `${refBase}_MCP_TOKEN`
      if (token) await setSecret(tokenRef, token.trim())
      const id = await createConnector({
        name: slug,
        kind: 'mcp',
        config: { endpoint: endpoint.trim() },
        credential_ref: token ? tokenRef : '',
        enabled: false,
      })
      setStatus('Testing connection…')
      const res = await testConnector(id)
      if (!res.ok) {
        // Keep the dialog open so the failure is readable; the card
        // already exists (disabled) behind it.
        setStatus(`Connection failed: ${res.error}. The connector was saved disabled — fix and Test from its card.`)
        setBusy(false)
        onRefresh()
        return
      }
      await patchConnector(id, { enabled: true })
      onAdded()
    } catch (err) {
      onError(errText(err))
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            <span className="flex items-center gap-3">
              <ConnectorLogo preset={preset} className="size-9" />
              <span>
                Add {preset.name}
                <span className="block text-xs font-normal text-muted-foreground">
                  kind: {preset.kind}
                </span>
              </span>
            </span>
          </DialogTitle>
        </DialogHeader>

        <div className="space-y-3">
          <Field label="Name (lowercase slug — prefixes this connector's tool names)">
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={preset.id === 'custom-mcp' ? 'my-server' : slugify(preset.name)}
              className="mt-1 h-8"
            />
          </Field>

          {isGoogle ? (
            <>
              <div className="grid gap-3 sm:grid-cols-2">
                <Field label="OAuth client ID">
                  <Input
                    value={clientID}
                    onChange={(e) => setClientID(e.target.value)}
                    placeholder="….apps.googleusercontent.com"
                    className="mt-1 h-8"
                  />
                </Field>
                <Field label="OAuth client secret">
                  <Input
                    type="password"
                    value={clientSecret}
                    onChange={(e) => setClientSecret(e.target.value)}
                    placeholder="GOCSPX-…"
                    className="mt-1 h-8"
                    autoComplete="off"
                  />
                </Field>
              </div>
              <p className="text-xs text-muted-foreground">
                From a Google Cloud OAuth client (Web application). Add
                <span className="font-mono"> {window.location.origin}/v1/connectors/oauth/callback </span>
                to its authorized redirect URIs. Scopes: {preset.scopes?.map((s) => s.split('/').pop()).join(', ')}.
                Saving redirects you to Google to consent.
              </p>
            </>
          ) : (
            <>
              <Field label="Endpoint">
                <Input
                  value={endpoint}
                  onChange={(e) => setEndpoint(e.target.value)}
                  placeholder="https://…/mcp"
                  className="mt-1 h-8"
                />
              </Field>
              {preset.endpointHint && (
                <p className="text-xs text-muted-foreground">{preset.endpointHint}</p>
              )}
              <Field label={preset.id === 'custom-mcp' ? 'Bearer token (optional)' : 'Bearer token'}>
                <Input
                  type="password"
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  placeholder={preset.tokenPlaceholder ?? 'token'}
                  className="mt-1 h-8"
                  autoComplete="off"
                />
              </Field>
              {preset.tokenHint && <p className="text-xs text-muted-foreground">{preset.tokenHint}</p>}
            </>
          )}

          {status && <p className="text-xs text-muted-foreground">{status}</p>}
        </div>

        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={!canSubmit} onClick={() => void submit()}>
            {busy ? 'Working…' : isGoogle ? 'Save & connect Google' : 'Add & test'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
