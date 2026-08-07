import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { listProviders, patchProvider, providersHealth, testProvider } from '../../api/client'
import type { AdminProvider, ProviderHealth, TestResult } from '../../api/types'
import { Button } from '../ui/button'
import { matchPreset, providerPresets } from './presets'
import { ProviderLogo } from './ProviderLogo'
import { Toggle } from './shared'
import { errText } from './util'

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
    <div className="mt-6 space-y-8">
      <section className="space-y-3">
        <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {providers.length > 0 ? `Your providers · ${providers.length}` : 'Your providers'}
        </h2>
        {providers.length === 0 ? (
          <div className="rounded-xl border border-dashed border-border p-10 text-center text-sm text-muted-foreground">
            No providers configured yet, add one below.
          </div>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
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

      <section className="space-y-3">
        <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Add a provider
        </h2>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {providerPresets.map((preset) => (
            <button
              key={preset.id}
              type="button"
              onClick={() => navigate(`/settings/providers/new/${preset.id}`)}
              className="flex items-center gap-3 rounded-xl border border-dashed border-border p-4 text-left transition hover:border-brand hover:bg-muted/50"
            >
              <ProviderLogo preset={preset} className="size-9" />
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
  const [testing, setTesting] = useState(false)
  const [test, setTest] = useState<TestResult | null>(null)

  const runTest = async () => {
    setTesting(true)
    setTest(null)
    try {
      setTest(await testProvider(provider.id))
    } catch (err) {
      setTest({ ok: false, latency_ms: 0, model: '', detail: errText(err) })
    } finally {
      setTesting(false)
    }
  }

  const toggle = (enabled: boolean) => {
    patchProvider(provider.id, { enabled }).then(onChanged, (err: unknown) =>
      toast.error('Could not update provider', { description: errText(err) }),
    )
  }

  return (
    <div className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4 shadow-sm transition hover:shadow-md">
      <div className="flex items-center gap-3">
        <ProviderLogo preset={preset} className="size-9" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-semibold">{provider.name}</div>
          <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
            {isCli ? (
              <>
                <span className="size-1.5 shrink-0 rounded-full bg-muted-foreground" />
                cli
              </>
            ) : (
              <>
                <span className={`size-1.5 shrink-0 rounded-full ${health?.healthy ? 'bg-good' : 'bg-destructive'}`} />
                {health?.healthy ? 'healthy' : 'credential missing'}
              </>
            )}
          </div>
        </div>
        <Toggle on={provider.enabled} onChange={toggle} label={`${provider.name} enabled`} />
      </div>

      {provider.default_model && (
        <div className="truncate text-xs text-muted-foreground">
          default <span className="font-mono text-foreground">{provider.default_model}</span>
        </div>
      )}

      {!isCli && test && (
        <div
          className={`rounded-lg border p-2 text-xs ${test.ok ? 'border-good/30 bg-good-soft text-good' : 'border-destructive/30 bg-destructive/5 text-destructive'}`}
        >
          {test.ok ? `OK, ${test.latency_ms} ms` : `Failed: ${test.detail}`}
        </div>
      )}

      <div className="mt-auto flex items-center gap-2 pt-1">
        {!isCli && (
          <Button size="sm" variant="test" disabled={testing} onClick={() => void runTest()} className="flex-1">
            {testing ? 'Testing…' : 'Test'}
          </Button>
        )}
        <Button size="sm" variant="outline" onClick={onManage} className="flex-1">
          Manage
        </Button>
      </div>
    </div>
  )
}
