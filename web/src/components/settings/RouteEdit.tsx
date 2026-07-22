import {
  ArrowDown01Icon,
  ArrowLeft01Icon,
  ArrowUp01Icon,
  Delete02Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { Link, Navigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { listProviders, listRoutes, patchRoute, providersHealth } from '../../api/client'
import type { AdminProvider, AdminRoute, ChainEntry, ProviderHealth } from '../../api/types'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../ui/select'
import { Field, Toggle } from './shared'
import { errText } from './util'

export function RouteEdit() {
  const { name } = useParams()
  const [route, setRoute] = useState<AdminRoute | null | undefined>(undefined)
  const [providers, setProviders] = useState<AdminProvider[]>([])
  const [health, setHealth] = useState<Record<string, ProviderHealth>>({})

  const refresh = useCallback(() => {
    Promise.all([listRoutes(), listProviders(), providersHealth()])
      .then(([routes, p, h]) => {
        setRoute(routes.find((r) => r.name === name) ?? null)
        setProviders(p)
        setHealth(Object.fromEntries(h.map((x) => [x.name, x])))
      })
      .catch((err: unknown) => toast.error('Could not load route', { description: errText(err) }))
  }, [name])
  useEffect(refresh, [refresh])

  if (route === null) return <Navigate to="/settings/routes" replace />
  if (route === undefined) return null

  const nameOf = (id: string) => providers.find((p) => p.id === id)?.name ?? id.slice(0, 8)
  const serving = route.enabled
    ? route.chain.find((e) => {
        const p = providers.find((x) => x.id === e.provider_id)
        return p?.enabled && health[p.name]?.healthy
      })
    : undefined

  const save = (patch: { chain?: ChainEntry[]; strategy?: string; enabled?: boolean }) => {
    patchRoute(route.name, patch).then(refresh, (err: unknown) =>
      toast.error('Could not update route', { description: errText(err) }),
    )
  }

  return (
    <div className="mt-6 w-full space-y-6">
      <Link
        to="/settings/routes"
        className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground transition hover:text-foreground"
      >
        <HugeiconsIcon icon={ArrowLeft01Icon} className="size-4" />
        Routes
      </Link>

      <div className="flex flex-wrap items-center gap-4 border-b border-border pb-6">
        <div className="min-w-0 flex-1">
          <h1 className="text-xl font-semibold tracking-tight">{route.name}</h1>
          {serving ? (
            <p className="text-sm text-muted-foreground">
              serving <span className="text-foreground">{nameOf(serving.provider_id)}</span> /{' '}
              <span className="font-mono text-foreground">{serving.model}</span>
            </p>
          ) : (
            <p className="text-sm font-medium text-warning">
              {route.enabled ? 'no usable provider' : 'disabled'}
            </p>
          )}
        </div>
        <Select value={route.strategy || 'ordered'} onValueChange={(v) => save({ strategy: v })}>
          <SelectTrigger className="h-10 w-36" aria-label={`${route.name} strategy`}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="ordered">Ordered</SelectItem>
            <SelectItem value="auto">Auto</SelectItem>
            <SelectItem value="price">Cheapest</SelectItem>
            <SelectItem value="latency">Fastest</SelectItem>
          </SelectContent>
        </Select>
        <Toggle on={route.enabled} onChange={(v) => save({ enabled: v })} label={`${route.name} route enabled`} />
      </div>

      <div className="max-w-2xl space-y-4">
        <h2 className="text-sm font-semibold">Chain</h2>
        <ol className="space-y-2">
          {route.chain.map((e, i) => (
            <li
              key={`${e.provider_id}-${e.model}-${i}`}
              className="flex items-center gap-3 rounded-lg border border-border px-3 py-2.5 text-sm"
            >
              <span className="w-5 text-xs text-muted-foreground">{i + 1}.</span>
              <span className="min-w-0 flex-1 truncate">
                {nameOf(e.provider_id)} / <span className="font-mono">{e.model}</span>
              </span>
              <span className="flex items-center gap-1">
                <button
                  type="button"
                  aria-label={`Move ${e.model} up`}
                  disabled={i === 0}
                  onClick={() => {
                    const chain = [...route.chain]
                    ;[chain[i - 1], chain[i]] = [chain[i], chain[i - 1]]
                    save({ chain })
                  }}
                  className="text-muted-foreground hover:text-foreground disabled:opacity-30"
                >
                  <HugeiconsIcon icon={ArrowUp01Icon} className="size-4" />
                </button>
                <button
                  type="button"
                  aria-label={`Move ${e.model} down`}
                  disabled={i === route.chain.length - 1}
                  onClick={() => {
                    const chain = [...route.chain]
                    ;[chain[i], chain[i + 1]] = [chain[i + 1], chain[i]]
                    save({ chain })
                  }}
                  className="text-muted-foreground hover:text-foreground disabled:opacity-30"
                >
                  <HugeiconsIcon icon={ArrowDown01Icon} className="size-4" />
                </button>
                <button
                  type="button"
                  aria-label={`Remove ${e.model}`}
                  onClick={() => save({ chain: route.chain.filter((_, j) => j !== i) })}
                  className="text-muted-foreground hover:text-destructive"
                >
                  <HugeiconsIcon icon={Delete02Icon} className="size-4" />
                </button>
              </span>
            </li>
          ))}
          {route.chain.length === 0 && (
            <li className="text-sm text-muted-foreground">No providers in this chain yet.</li>
          )}
        </ol>
        <AddChainEntry providers={providers} onAdd={(entry) => save({ chain: [...route.chain, entry] })} />
      </div>
    </div>
  )
}

// customModel is the sentinel Select value that switches the model
// picker to free-text entry — declared model ids never collide with it.
const customModel = '·custom·'

function AddChainEntry({
  providers,
  onAdd,
}: {
  providers: AdminProvider[]
  onAdd: (e: ChainEntry) => void
}) {
  const [providerID, setProviderID] = useState('')
  const [model, setModel] = useState('')
  const [manual, setManual] = useState(false)

  const selected = providers.find((x) => x.id === providerID)
  const declared = selected?.models.map((m) => m.id) ?? []

  return (
    <Field label="Add a provider to this chain">
      <div className="mt-1.5 flex flex-wrap items-center gap-2">
        <Select
          value={providerID}
          onValueChange={(id) => {
            setProviderID(id)
            const p = providers.find((x) => x.id === id)
            setManual((p?.models.length ?? 0) === 0)
            setModel(p?.default_model ?? '')
          }}
        >
          <SelectTrigger className="h-10 w-44" aria-label="Provider">
            <SelectValue placeholder="provider…" />
          </SelectTrigger>
          <SelectContent>
            {providers.map((p) => (
              <SelectItem key={p.id} value={p.id}>
                {p.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {manual || declared.length === 0 ? (
          <Input
            aria-label="Model"
            placeholder="model id"
            value={model}
            onChange={(e) => setModel(e.target.value)}
            className="h-10 w-56"
          />
        ) : (
          <Select
            value={declared.includes(model) ? model : ''}
            onValueChange={(v) => {
              if (v === customModel) {
                setManual(true)
                setModel('')
                return
              }
              setModel(v)
            }}
          >
            <SelectTrigger className="h-10 w-56" aria-label="Model">
              <SelectValue placeholder="model…" />
            </SelectTrigger>
            <SelectContent>
              {declared.map((id) => (
                <SelectItem key={id} value={id}>
                  {id}
                </SelectItem>
              ))}
              <SelectItem value={customModel}>custom…</SelectItem>
            </SelectContent>
          </Select>
        )}
        <Button
          variant="outline"
          disabled={!providerID || !model}
          onClick={() => {
            onAdd({ provider_id: providerID, model })
            setModel('')
          }}
        >
          Add
        </Button>
      </div>
    </Field>
  )
}
