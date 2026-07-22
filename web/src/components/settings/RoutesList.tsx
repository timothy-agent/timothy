import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { listProviders, listRoutes, patchRoute, providersHealth } from '../../api/client'
import type { AdminProvider, AdminRoute, ChainEntry, ProviderHealth } from '../../api/types'
import { Toggle } from './shared'
import { errText } from './util'

export function RoutesList() {
  const [routes, setRoutes] = useState<AdminRoute[]>([])
  const [providers, setProviders] = useState<AdminProvider[]>([])
  const [health, setHealth] = useState<Record<string, ProviderHealth>>({})
  const navigate = useNavigate()

  const refresh = useCallback(() => {
    Promise.all([listRoutes(), listProviders(), providersHealth()])
      .then(([r, p, h]) => {
        setRoutes(r)
        setProviders(p)
        setHealth(Object.fromEntries(h.map((x) => [x.name, x])))
      })
      .catch((err: unknown) => toast.error('Could not load routes', { description: errText(err) }))
  }, [])
  useEffect(refresh, [refresh])

  const nameOf = (id: string) => providers.find((p) => p.id === id)?.name ?? id.slice(0, 8)

  // servingEntry mirrors the router: first chain entry whose provider
  // is enabled and credential-healthy serves the request.
  const servingEntry = (r: AdminRoute): ChainEntry | undefined =>
    r.enabled
      ? r.chain.find((e) => {
          const p = providers.find((x) => x.id === e.provider_id)
          return p?.enabled && health[p.name]?.healthy
        })
      : undefined

  const toggle = (r: AdminRoute, enabled: boolean) => {
    patchRoute(r.name, { enabled }).then(refresh, (err: unknown) =>
      toast.error('Could not update route', { description: errText(err) }),
    )
  }

  return (
    <div className="mt-6 space-y-4">
      <p className="text-sm text-muted-foreground">
        Routes are named model chains agents route through — reorder providers within a route or
        add fallbacks from its own page.
      </p>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {routes.map((r) => {
          const serving = servingEntry(r)
          return (
            <button
              key={r.name}
              type="button"
              onClick={() => navigate(`/settings/routes/${r.name}`)}
              className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4 text-left shadow-sm transition hover:shadow-md"
            >
              <div className="flex items-center gap-3">
                <span className="truncate text-sm font-semibold">{r.name}</span>
                <span className="ml-auto rounded bg-muted px-1.5 py-0.5 text-xs font-medium capitalize text-muted-foreground">
                  {r.strategy || 'ordered'}
                </span>
                <span onClick={(e) => e.stopPropagation()}>
                  <Toggle on={r.enabled} onChange={(v) => toggle(r, v)} label={`${r.name} route enabled`} />
                </span>
              </div>
              {serving ? (
                <p className="text-xs text-muted-foreground">
                  serving <span className="text-foreground">{nameOf(serving.provider_id)}</span> /{' '}
                  <span className="font-mono text-foreground">{serving.model}</span>
                </p>
              ) : (
                <p className="text-xs font-medium text-warning">
                  {r.enabled ? 'no usable provider' : 'disabled'}
                </p>
              )}
              <p className="text-xs text-muted-foreground">{r.chain.length} provider(s) in chain</p>
            </button>
          )
        })}
      </div>
    </div>
  )
}
