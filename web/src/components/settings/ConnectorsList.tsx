import { useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { listConnectors, patchConnector, testConnector } from '../../api/client'
import type { AdminConnector, GitHubIdentity } from '../../api/types'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Switch } from '../ui/switch'
import { Alert, AlertDescription } from '../ui/alert'
import { EmptyState } from '../timothy/empty-state'
import { PageHeader, SectionHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { AddPresetTile } from './AddPresetTile'
import { EntityCard } from './EntityCard'
import { ConnectorLogo } from './ConnectorLogo'
import { connectorPresets, presetFor } from './connectorPresets'
import { settingsArea } from './settingsAreas'
import { TestStatus } from './TestStatus'
import { connectedAs } from './util'
import { errText, isTimothyAuthError } from '../../lib/errors'

const area = settingsArea('connectors')

export function ConnectorsList() {
  const [connectors, setConnectors] = useState<AdminConnector[]>([])
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
    <PageShell>
      <PageHeader
        title={area.label}
        description={area.description}
        breadcrumbs={[{ label: 'Settings', href: '/settings' }, { label: area.label }]}
      />
      <div className="space-y-10">
        {oauthConnected && (
          <Alert tone="good">
            <AlertDescription className="flex items-center gap-3">
              <span>Account connected to “{oauthConnected}”. Enable it below to serve tools.</span>
              <button type="button" onClick={clearOAuthParams} className="ml-auto text-sm underline-offset-2 hover:underline">
                dismiss
              </button>
            </AlertDescription>
          </Alert>
        )}
        {oauthError && (
          <Alert tone="destructive">
            <AlertDescription className="flex items-center gap-3">
              <span>Connection failed: {oauthError}</span>
              <button type="button" onClick={clearOAuthParams} className="ml-auto text-sm underline-offset-2 hover:underline">
                dismiss
              </button>
            </AlertDescription>
          </Alert>
        )}

        <section className="space-y-4">
          <SectionHeader title={connectors.length > 0 ? `Your connectors · ${connectors.length}` : 'Your connectors'} />
          <p className="-mt-2 max-w-2xl text-sm text-muted-foreground">
            Integrations the agent can use as tools. A tool appears to the model once per capability
            (e.g. <span className="font-mono text-xs">search_mail</span>) with an{' '}
            <span className="font-mono text-xs">account</span> argument routing to the right
            connector when more than one serves it; a name that would otherwise collide (with a
            built-in tool, or across two MCP servers with different schemas) keeps its{' '}
            <span className="font-mono text-xs">name_tool</span> form instead. Either way, tool
            calls go through the same permission prompts as everything else.
          </p>
          {connectors.length === 0 ? (
            <EmptyState title="No connectors yet" description="Add one below." />
          ) : (
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {connectors.map((c) => (
                <ConnectorCard key={c.id} connector={c} onChanged={refresh} />
              ))}
            </div>
          )}
        </section>

        <section className="space-y-4">
          <SectionHeader title="Add a connector" />
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {connectorPresets.map((preset) => (
              <AddPresetTile
                key={preset.id}
                to={`/settings/connectors/new/${preset.id}`}
                title={preset.name}
                description={preset.description}
                tile={<ConnectorLogo preset={preset} className="size-9" />}
              />
            ))}
          </div>
        </section>
      </div>
    </PageShell>
  )
}

function ConnectorCard({
  connector,
  onChanged,
}: {
  connector: AdminConnector
  onChanged: () => void
}) {
  const preset = presetFor(connector)
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

  const summary =
    connector.kind === 'mcp'
      ? String(connector.config.endpoint ?? '')
      : connector.kind === 'google' || connector.kind === 'microsoft'
        ? (connector.config.scopes as string[] | undefined)?.map((s) => s.split('/').pop()).join(', ')
        : connector.kind === 'imap' || connector.kind === 'caldav'
          ? String(connector.config.username ?? '')
          : 'Identity for mission use, read-only pull request tools'

  return (
    <EntityCard
      to={`/settings/connectors/${connector.id}`}
      title={connector.name}
      tile={<ConnectorLogo preset={preset} className="size-9" />}
      badges={connector.sensitive && <Badge variant="warning">Sensitive</Badge>}
      summary={<div className="truncate text-xs text-muted-foreground">{summary}</div>}
      status={
        test && (
          <TestStatus
            state={test.ok ? 'ok' : 'failed'}
            message={test.ok ? (test.identity ? connectedAs(test.identity) : 'Connection OK') : `Failed: ${test.error}`}
          />
        )
      }
      footer={
        <>
          <Switch checked={connector.enabled} onCheckedChange={toggle} aria-label={`${connector.name} enabled`} />
          <Button size="sm" variant="test" disabled={testing} onClick={() => void runTest()} className="flex-1">
            {testing ? 'Testing…' : 'Test'}
          </Button>
        </>
      }
    />
  )
}
