import { Plus, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
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
import { BrandTile } from '../timothy/brand-tile'
import { IconButton } from '../timothy/icon-button'
import { Panel } from '../timothy/panel'
import { PageHeader, SectionHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Label } from '../ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Switch } from '../ui/switch'
import { EntityCard } from './EntityCard'
import { ServingLine } from './ServingLine'
import { settingsArea } from './settingsAreas'
import { UNSET } from './util'
import { errText } from '../../lib/errors'

const area = settingsArea('routes')

const ROLES = [
  { key: 'default', label: 'Chat (default)' },
  { key: 'embedding', label: 'Embeddings' },
  { key: 'vision', label: 'Vision' },
  { key: 'summarize', label: 'Summarize' },
]
const CAPABILITIES = ['chat', 'embeddings', 'vision']

export function RoutesList() {
  const [routes, setRoutes] = useState<AdminRoute[]>([])
  const [providers, setProviders] = useState<AdminProvider[]>([])
  const [newName, setNewName] = useState('')
  const [newCapability, setNewCapability] = useState('chat')

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
    <PageShell>
      <PageHeader
        title={area.label}
        description={area.description}
        breadcrumbs={[{ label: 'Settings', href: '/settings' }, { label: area.label }]}
      />
      <div className="space-y-10">
        <Panel title="System roles" description="Timothy needs one route bound to each of these 4 roles to work. A newly connected provider fills in whichever are still unbound.">
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            {ROLES.map((role) => {
              const bound = routes.find((r) => r.role === role.key)
              return (
                <div key={role.key} className="space-y-2">
                  <Label htmlFor={`role-${role.key}`}>{role.label}</Label>
                  <Select
                    value={bound?.name ?? UNSET}
                    onValueChange={(v) => {
                      if (v !== UNSET) assignRole(role.key, v)
                    }}
                  >
                    <SelectTrigger id={`role-${role.key}`} className="w-full" aria-label={`${role.label} route`}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {!bound && <SelectItem value={UNSET}>Unbound</SelectItem>}
                      {routes.map((r) => (
                        <SelectItem key={r.name} value={r.name}>
                          {r.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              )
            })}
          </div>
        </Panel>

        <section className="space-y-4">
          <SectionHeader
            title={routes.length > 0 ? `Your routes · ${routes.length}` : 'Your routes'}
            description="Routes are named model chains agents route through, reorder providers within a route or add fallbacks from its own page."
          />
          <div className="flex flex-wrap items-end gap-2">
            <div className="space-y-2">
              <Label htmlFor="new-route-name">New route name</Label>
              <Input
                id="new-route-name"
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                placeholder="my-route"
                className="w-40"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="new-route-capability">Capability</Label>
              <Select value={newCapability} onValueChange={setNewCapability}>
                <SelectTrigger id="new-route-capability" className="w-32" aria-label="New route capability">
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
            </div>
            <Button onClick={create} disabled={!newName.trim()}>
              <Plus className="size-4" />
              Add route
            </Button>
          </div>

          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {routes.map((r) => (
              <RouteCard key={r.name} route={r} nameOf={nameOf} onToggle={(v) => toggle(r, v)} onDelete={() => remove(r)} />
            ))}
          </div>
        </section>
      </div>
    </PageShell>
  )
}

function RouteCard({
  route: r,
  nameOf,
  onToggle,
  onDelete,
}: {
  route: AdminRoute
  nameOf: (id: string) => string
  onToggle: (v: boolean) => void
  onDelete: () => void
}) {
  return (
    <EntityCard
      to={`/settings/routes/${r.name}`}
      title={r.name}
      tile={<BrandTile size="sm" />}
      badges={
        <>
          {r.role && <Badge variant="brand">{r.role}</Badge>}
          <Badge variant="neutral" className="capitalize">
            {r.strategy || 'ordered'}
          </Badge>
        </>
      }
      summary={<ServingLine route={r} nameOf={nameOf} />}
      status={
        r.chain.length > 0 ? (
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
        )
      }
      footer={
        <>
          <Switch checked={r.enabled} onCheckedChange={onToggle} aria-label={`${r.name} route enabled`} />
          <IconButton
            label={r.role ? `Serves the ${r.role} role, reassign that role first` : `Delete ${r.name} route`}
            icon={Trash2}
            variant="ghost"
            size="sm"
            disabled={!!r.role}
            onClick={onDelete}
            className="ml-auto"
          />
        </>
      }
    />
  )
}
