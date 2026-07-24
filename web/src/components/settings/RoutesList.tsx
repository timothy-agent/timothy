import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { listProviders, listRoutes, patchRoute } from '../../api/client'
import type { AdminProvider, AdminRoute } from '../../api/types'
import { Toggle } from './shared'
import { errText } from './util'

export function RoutesList() {
  const [routes, setRoutes] = useState<AdminRoute[]>([])
  const [providers, setProviders] = useState<AdminProvider[]>([])
  const navigate = useNavigate()

  const refresh = useCallback(() => {
    Promise.all([listRoutes(), listProviders()])
      .then(([r, p]) => {
        setRoutes(r)
        setProviders(p)
      })
      .catch((err: unknown) => toast.error('Could not load routes', { description: errText(err) }))
  }, [])
  useEffect(refresh, [refresh])

  const nameOf = (id: string) => providers.find((p) => p.id === id)?.name ?? id.slice(0, 8)

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
          // The router's own verdict: first usable entry in try order.
          const serving = r.serving
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
              ) : !r.enabled ? (
                <p className="text-xs font-medium text-warning">disabled</p>
              ) : r.resolved ? (
                <p className="text-xs font-medium text-warning">no usable provider</p>
              ) : (
                <p className="text-xs text-muted-foreground">stats loading…</p>
              )}
              <p className="text-xs text-muted-foreground">{r.chain.length} provider(s) in chain</p>
            </button>
          )
        })}
      </div>
    </div>
  )
}
