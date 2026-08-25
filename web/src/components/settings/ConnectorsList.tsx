import { useCallback, useEffect, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { listConnectors, patchConnector, testConnector } from '../../api/client'
import type { AdminConnector, GitHubIdentity } from '../../api/types'
import { Button } from '../ui/button'
import { ConnectorLogo } from './ConnectorLogo'
import { connectorPresets, unknownPreset } from './connectorPresets'
import { Toggle } from './shared'
import { errText, isTimothyAuthError } from './util'

export function ConnectorsList() {
  const [connectors, setConnectors] = useState<AdminConnector[]>([])
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()

  // The OAuth callback bounces back here with the outcome in the query.
  const oauthConnected = params.get('oauth_connected')
  const oauthError = params.get('oauth_error')
  const clearOAuthParams = () => setParams({}, { replace: true })

  const refresh = useCallback(() => {
    listConnectors()
      .then(setConnectors)
      .catch((err: unknown) => toast.error('Could not load connectors', { description: errText(err) }))
  }, [])
  useEffect(refresh, [refresh])

  return (
    <div className="mt-6 space-y-8">
      {oauthConnected && (
        <div className="flex items-center gap-3 rounded-xl border border-good/30 bg-good-soft p-3 text-sm text-good">
          <span>Google account connected to “{oauthConnected}”. Enable it below to serve tools.</span>
          <button type="button" onClick={clearOAuthParams} className="ml-auto text-sm underline-offset-2 hover:underline">
            dismiss
          </button>
        </div>
      )}
      {oauthError && (
        <div className="flex items-center gap-3 rounded-xl border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">
          <span>Google connection failed: {oauthError}</span>
          <button type="button" onClick={clearOAuthParams} className="ml-auto text-sm underline-offset-2 hover:underline">
            dismiss
          </button>
        </div>
      )}

      <section className="space-y-3">
        <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {connectors.length > 0 ? `Your connectors · ${connectors.length}` : 'Your connectors'}
        </h2>
        <p className="text-sm text-muted-foreground">
          Integrations the agent can use as tools, each connector&apos;s tools appear to the
          model as <span className="font-mono text-xs">name_tool</span> and go through the same
          permission prompts as everything else.
        </p>
        {connectors.length === 0 ? (
          <div className="rounded-xl border border-dashed border-border p-10 text-center text-sm text-muted-foreground">
            No connectors yet, add one below.
          </div>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {connectors.map((c) => (
              <ConnectorCard
                key={c.id}
                connector={c}
                onChanged={refresh}
                onManage={() => navigate(`/settings/connectors/${c.id}`)}
              />
            ))}
          </div>
        )}
      </section>

      <section className="space-y-3">
        <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Add a connector
        </h2>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {connectorPresets.map((preset) => (
            <button
              key={preset.id}
              type="button"
              onClick={() => navigate(`/settings/connectors/new/${preset.id}`)}
              className="flex items-center gap-3 rounded-xl border border-dashed border-border p-4 text-left transition hover:border-brand hover:bg-muted/50"
            >
              <ConnectorLogo preset={preset} className="size-9" />
              <span className="min-w-0">
                <span className="block text-sm font-semibold">{preset.name}</span>
                <span className="block truncate text-sm text-muted-foreground">
                  {preset.description}
                </span>
              </span>
            </button>
          ))}
        </div>
      </section>
    </div>
  )
}

function ConnectorCard({
  connector,
  onChanged,
  onManage,
}: {
  connector: AdminConnector
  onChanged: () => void
  onManage: () => void
}) {
  const preset = connectorPresets.find((p) => matchesPreset(connector, p)) ?? unknownPreset
  const [testing, setTesting] = useState(false)
  const [test, setTest] = useState<{ ok: boolean; error?: string; identity?: GitHubIdentity } | null>(
    null,
  )

  const toggle = (enabled: boolean) => {
    patchConnector(connector.id, { enabled }).then(onChanged, (err: unknown) =>
      toast.error('Could not update connector', { description: errText(err) }),
    )
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

  return (
    <div className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4 shadow-sm transition hover:shadow-md">
      <div className="flex items-center gap-3">
        <ConnectorLogo preset={preset} className="size-9" />
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <div className="truncate text-sm font-semibold">{connector.name}</div>
            {connector.sensitive && (
              <span className="shrink-0 rounded-full border border-amber-500/30 bg-amber-500/10 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-amber-600 dark:text-amber-400">
                Sensitive
              </span>
            )}
          </div>
          <div className="text-xs text-muted-foreground uppercase">{connector.kind}</div>
        </div>
        <Toggle on={connector.enabled} onChange={toggle} label={`${connector.name} enabled`} />
      </div>

      <div className="truncate text-xs text-muted-foreground">
        {connector.kind === 'mcp'
          ? String(connector.config.endpoint ?? '')
          : connector.kind === 'google' || connector.kind === 'microsoft'
            ? (connector.config.scopes as string[] | undefined)?.map((s) => s.split('/').pop()).join(', ')
            : 'Identity for mission use, no chat tools'}
      </div>

      {test && (
        <div
          className={`rounded-lg border p-2 text-xs ${test.ok ? 'border-good/30 bg-good-soft text-good' : 'border-destructive/30 bg-destructive/5 text-destructive'}`}
        >
          {test.ok
            ? test.identity
              ? `Connected as ${test.identity.login} (${test.identity.email})`
              : 'Connection OK'
            : `Failed: ${test.error}`}
        </div>
      )}

      <div className="mt-auto flex items-center gap-2 pt-1">
        <Button size="sm" variant="test" disabled={testing} onClick={() => void runTest()} className="flex-1">
          {testing ? 'Testing…' : 'Test'}
        </Button>
        <Button size="sm" variant="outline" onClick={onManage} className="flex-1">
          Manage
        </Button>
      </div>
    </div>
  )
}

function matchesPreset(
  c: AdminConnector,
  p: { kind: 'mcp' | 'google' | 'github' | 'microsoft'; scopes?: string[]; endpoint?: string },
) {
  if (p.kind !== c.kind) return false
  if (c.kind === 'google' || c.kind === 'microsoft') {
    const scopes = JSON.stringify(c.config.scopes ?? '')
    return p.scopes?.every((s) => scopes.includes(s)) ?? false
  }
  if (c.kind === 'github') return true
  const endpoint = String(c.config.endpoint ?? '')
  return !!p.endpoint && endpoint.startsWith(p.endpoint)
}
