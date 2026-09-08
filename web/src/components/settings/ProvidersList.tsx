import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { catalogStatus, listProviders, patchProvider, providersHealth, refreshCatalog } from '../../api/client'
import type { AdminProvider, CatalogSyncStatus, ProviderHealth } from '../../api/types'
import { relativeTime } from '../../lib/format'
import { PageHeader, SectionHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { EmptyState } from '../timothy/empty-state'
import { Button } from '../ui/button'
import { Switch } from '../ui/switch'
import { AddPresetTile } from './AddPresetTile'
import { EntityCard } from './EntityCard'
import { matchPreset, providerPresets } from '../../lib/providerPresets'
import { ProviderLogo } from '../timothy/provider-logo'
import { settingsArea } from './settingsAreas'
import { TestStatus } from './TestStatus'
import { useProviderTest } from './useProviderTest'
import { errText } from '../../lib/errors'

const area = settingsArea('providers')

export function ProvidersList() {
  const [providers, setProviders] = useState<AdminProvider[]>([])
  const [health, setHealth] = useState<Record<string, ProviderHealth>>({})
  const navigate = useNavigate()

  const refresh = useCallback(() => {
    Promise.all([listProviders(), providersHealth()])
      .then(([list, rows]) => {
        setProviders(list)
        setHealth(Object.fromEntries(rows.map((h) => [h.name, h])))
      })
      .catch((err: unknown) => toast.error('Could not load providers', { description: errText(err) }))
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
        <CatalogStatusLine />

        <section className="space-y-4">
          <SectionHeader title={providers.length > 0 ? `Your providers · ${providers.length}` : 'Your providers'} />
          {providers.length === 0 ? (
            <EmptyState title="No providers configured yet" description="Add one below to route work to it." />
          ) : (
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {providers.map((p) => (
                <ProviderCard
                  key={p.id}
                  provider={p}
                  health={health[p.name]}
                  onChanged={refresh}
                  onManage={() => navigate(`/settings/providers/${p.id}`)}
                />
              ))}
            </div>
          )}
        </section>

        <section className="space-y-4">
          <SectionHeader title="Add a provider" />
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {providerPresets.map((preset) => (
              <AddPresetTile
                key={preset.id}
                to={`/settings/providers/new/${preset.id}`}
                title={preset.name}
                description={preset.description}
                tile={<ProviderLogo preset={preset} className="size-9" />}
              />
            ))}
          </div>
        </section>
      </div>
    </PageShell>
  )
}

// ProviderCard is a compact status summary: enough to see at a glance
// whether it's healthy and serving, with heavier editing (keys,
// models) living on its own Manage page.
function ProviderCard({
  provider,
  health,
  onChanged,
  onManage,
}: {
  provider: AdminProvider
  health?: ProviderHealth
  onChanged: () => void
  onManage: () => void
}) {
  const preset = matchPreset(provider)
  const isCli = provider.kind === 'cli'
  const test = useProviderTest(provider.id)

  const toggle = (enabled: boolean) => {
    patchProvider(provider.id, { enabled }).then(onChanged, (err: unknown) =>
      toast.error('Could not update provider', { description: errText(err) }),
    )
  }

  const healthLabel = isCli
    ? `subscription · ${health?.healthy ? 'healthy' : 'auth failed'}`
    : health?.healthy
      ? 'healthy'
      : 'credential missing'

  return (
    <EntityCard
      to={`/settings/providers/${provider.id}`}
      title={provider.name}
      tile={<ProviderLogo preset={preset} className="size-9" />}
      summary={
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <span className={`size-1.5 shrink-0 rounded-full ${health?.healthy ? 'bg-good' : 'bg-destructive'}`} />
          {healthLabel}
          {provider.default_model && (
            <span className="truncate">
              · default <span className="font-mono text-foreground">{provider.default_model}</span>
            </span>
          )}
        </div>
      }
      status={!isCli && test.state !== 'idle' ? <TestStatus state={test.state} message={test.message} detail={test.detail} /> : undefined}
      footer={
        <>
          <Switch checked={provider.enabled} onCheckedChange={toggle} aria-label={`${provider.name} enabled`} />
          {!isCli && (
            <Button
              size="sm"
              variant="test"
              disabled={test.state === 'testing'}
              onClick={() => void test.run()}
              className="flex-1"
            >
              {test.state === 'testing' ? 'Testing…' : 'Test'}
            </Button>
          )}
          <Button size="sm" variant="outline" onClick={onManage} className="flex-1">
            Manage
          </Button>
        </>
      }
    />
  )
}

// CatalogStatusLine reports the last model catalog sync, the local
// cache of known models + pricing "Suggest from catalog" (on each
// provider's Manage page) matches against, with a manual Refresh.
function CatalogStatusLine() {
  const [status, setStatus] = useState<CatalogSyncStatus | null>(null)
  const [refreshing, setRefreshing] = useState(false)

  const refresh = useCallback(() => {
    catalogStatus()
      .then(setStatus)
      .catch(() => setStatus(null))
  }, [])
  useEffect(refresh, [refresh])

  const runRefresh = async () => {
    setRefreshing(true)
    try {
      setStatus(await refreshCatalog())
      toast.success('Model catalog refreshed')
    } catch (err) {
      toast.error('Could not refresh model catalog', { description: errText(err) })
    } finally {
      setRefreshing(false)
    }
  }

  if (!status) return null

  return (
    <div className="flex items-center justify-between rounded-md border border-border bg-muted/40 px-4 py-2.5 text-sm text-muted-foreground">
      <span>
        Model catalog: {status.entry_count.toLocaleString()} models
        {status.fetched_at ? `, synced ${relativeTime(status.fetched_at)}` : ', never synced'}
        {status.error && <span className="text-destructive"> · last refresh failed: {status.error}</span>}
      </span>
      <Button size="sm" variant="outline" disabled={refreshing} onClick={() => void runRefresh()}>
        {refreshing ? 'Refreshing…' : 'Refresh'}
      </Button>
    </div>
  )
}
