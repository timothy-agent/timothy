import { Delete02Icon, PlusSignIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import {
  createRoute,
  deleteRoute,
  listProviders,
  listRoutes,
  patchRoute,
  setRouteRole,
} from '../../api/client'
import type { AdminProvider, AdminRoute } from '../../api/types'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Toggle } from './shared'
import { errText } from './util'

const ROLES = [
  { key: 'default', label: 'Chat (default)' },
  { key: 'embedding', label: 'Embeddings' },
  { key: 'vision', label: 'Vision' },
  { key: 'summarize', label: 'Summarize' },
]
const CAPABILITIES = ['chat', 'embeddings', 'vision']
const ROLE_UNSET = '__unset__'

export function RoutesList() {
  const [routes, setRoutes] = useState<AdminRoute[]>([])
  const [providers, setProviders] = useState<AdminProvider[]>([])
  const [newName, setNewName] = useState('')
  const [newCapability, setNewCapability] = useState('chat')
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

  const create = () => {
    const name = newName.trim()
    if (!name) return
    createRoute(name, newCapability)
      .then(() => {
        setNewName('')
        refresh()
      })
      .catch((err: unknown) => toast.error('Could not create route', { description: errText(err) }))
  }

  const remove = (r: AdminRoute) => {
    deleteRoute(r.name).then(refresh, (err: unknown) =>
      toast.error('Could not delete route', { description: errText(err) }),
    )
  }

  const assignRole = (role: string, name: string) => {
    setRouteRole(name, role).then(refresh, (err: unknown) =>
      toast.error('Could not assign role', { description: errText(err) }),
    )
  }

  return (
    <div className="mt-6 space-y-6">
      <div className="rounded-xl border border-border p-4">
        <div className="text-sm font-medium">System roles</div>
        <p className="mt-0.5 text-xs text-muted-foreground">
          Timothy needs one route bound to each of these 4 roles to work. A newly connected
          provider fills in whichever are still unbound.
        </p>
        <div className="mt-3 grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          {ROLES.map((role) => {
            const bound = routes.find((r) => r.role === role.key)
            return (
              <label key={role.key} className="grid gap-1 text-xs text-muted-foreground">
                {role.label}
                <Select
                  value={bound?.name ?? ROLE_UNSET}
                  onValueChange={(v) => {
                    if (v !== ROLE_UNSET) assignRole(role.key, v)
                  }}
                >
                  <SelectTrigger className="h-10 w-full" aria-label={`${role.label} route`}>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {!bound && <SelectItem value={ROLE_UNSET}>Unbound</SelectItem>}
                    {routes.map((r) => (
                      <SelectItem key={r.name} value={r.name}>
                        {r.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </label>
            )
          })}
        </div>
      </div>

      <div className="space-y-4">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <p className="text-sm text-muted-foreground">
            Routes are named model chains agents route through, reorder providers within a route
            or add fallbacks from its own page.
          </p>
          <div className="flex items-end gap-2">
            <label className="grid gap-1 text-xs text-muted-foreground">
              New route name
              <Input
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                placeholder="my-route"
                className="w-40"
                aria-label="New route name"
              />
            </label>
            <label className="grid gap-1 text-xs text-muted-foreground">
              Capability
              <Select value={newCapability} onValueChange={setNewCapability}>
                <SelectTrigger className="h-9 w-32" aria-label="New route capability">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {CAPABILITIES.map((c) => (
                    <SelectItem key={c} value={c}>
                      {c}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </label>
            <Button onClick={create} disabled={!newName.trim()}>
              <HugeiconsIcon icon={PlusSignIcon} className="size-4" />
              Add route
            </Button>
          </div>
        </div>

        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {routes.map((r) => {
            // The router's own verdict: first usable entry in try order.
            const serving = r.serving
            return (
              <div
                key={r.name}
                className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4 text-left shadow-sm transition hover:shadow-md"
              >
                <button
                  type="button"
                  onClick={() => navigate(`/settings/routes/${r.name}`)}
                  className="flex items-center gap-3 text-left"
                >
                  <span className="truncate text-sm font-semibold">{r.name}</span>
                  {r.role && (
                    <span className="rounded bg-blue-500/10 px-1.5 py-0.5 text-xs font-medium text-blue-600 dark:text-blue-400">
                      {r.role}
                    </span>
                  )}
                  <span className="rounded bg-muted px-1.5 py-0.5 text-xs font-medium capitalize text-muted-foreground">
                    {r.strategy || 'ordered'}
                  </span>
                  <span className="ml-auto flex items-center gap-2" onClick={(e) => e.stopPropagation()}>
                    <Toggle on={r.enabled} onChange={(v) => toggle(r, v)} label={`${r.name} route enabled`} />
                    <button
                      type="button"
                      onClick={() => remove(r)}
                      disabled={!!r.role}
                      title={r.role ? `Serves the ${r.role} role, reassign that role first` : 'Delete route'}
                      aria-label={`Delete ${r.name} route`}
                      className="rounded p-1 text-muted-foreground hover:text-red-600 disabled:opacity-30 disabled:hover:text-muted-foreground"
                    >
                      <HugeiconsIcon icon={Delete02Icon} className="size-4" />
                    </button>
                  </span>
                </button>
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
                {r.chain.length > 0 ? (
                  <ol className="space-y-0.5 text-xs text-muted-foreground">
                    {r.chain.map((c, i) => (
                      <li key={`${c.provider_id}-${c.model}-${i}`} className="truncate">
                        {i + 1}. {nameOf(c.provider_id)} /{' '}
                        {/* An empty entry model follows the provider's default;
                            show what the router actually resolved it to. */}
                        <span className="font-mono" title={c.model ? undefined : 'provider default'}>
                          {c.model || r.resolved?.[i]?.model || ''}
                        </span>
                      </li>
                    ))}
                  </ol>
                ) : (
                  <p className="text-xs text-muted-foreground">no providers in chain</p>
                )}
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
