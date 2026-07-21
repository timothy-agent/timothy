import { ArrowDown01Icon, ArrowUp01Icon, Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
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
import { ErrorBanner, Toggle } from './shared'
import { errText } from './util'

export function RoutesTab() {
  const [routes, setRoutes] = useState<AdminRoute[]>([])
  const [providers, setProviders] = useState<AdminProvider[]>([])
  const [health, setHealth] = useState<Record<string, ProviderHealth>>({})
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(() => {
    Promise.all([listRoutes(), listProviders(), providersHealth()])
      .then(([r, p, h]) => {
        setRoutes(r)
        setProviders(p)
        setHealth(Object.fromEntries(h.map((x) => [x.name, x])))
        setError(null)
      })
      .catch((err: unknown) => setError(errText(err)))
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

  const save = (category: string, patch: { chain?: ChainEntry[]; enabled?: boolean }) => {
    patchRoute(category, patch).then(refresh, (err: unknown) => setError(errText(err)))
  }

  return (
    <div className="mt-6 space-y-4">
      <ErrorBanner message={error} />
      {routes.map((r) => {
        const serving = servingEntry(r)
        return (
          <div key={r.task_category} className="rounded-xl border border-border p-4">
            <div className="flex items-center gap-3">
              <span className="font-medium">{r.task_category}</span>
              {serving ? (
                <span className="text-xs text-muted-foreground">
                  serving: {nameOf(serving.provider_id)} / {serving.model}
                </span>
              ) : (
                <span className="text-xs text-amber-600 dark:text-amber-400">
                  {r.enabled ? 'no usable provider' : 'disabled'}
                </span>
              )}
              <div className="ml-auto">
                <Toggle
                  on={r.enabled}
                  onChange={(v) => save(r.task_category, { enabled: v })}
                  label={`${r.task_category} route enabled`}
                />
              </div>
            </div>
            <ol className="mt-3 space-y-1.5">
              {r.chain.map((e, i) => (
                <li key={`${e.provider_id}-${e.model}-${i}`} className="flex items-center gap-2 text-sm">
                  <span className="w-5 text-xs text-muted-foreground">{i + 1}.</span>
                  <span className="truncate">
                    {nameOf(e.provider_id)} / {e.model}
                  </span>
                  <span className="ml-auto flex items-center gap-1">
                    <button
                      type="button"
                      aria-label={`Move ${e.model} up`}
                      disabled={i === 0}
                      onClick={() => {
                        const chain = [...r.chain]
                        ;[chain[i - 1], chain[i]] = [chain[i], chain[i - 1]]
                        save(r.task_category, { chain })
                      }}
                      className="text-muted-foreground hover:text-foreground disabled:opacity-30"
                    >
                      <HugeiconsIcon icon={ArrowUp01Icon} className="size-4" />
                    </button>
                    <button
                      type="button"
                      aria-label={`Move ${e.model} down`}
                      disabled={i === r.chain.length - 1}
                      onClick={() => {
                        const chain = [...r.chain]
                        ;[chain[i], chain[i + 1]] = [chain[i + 1], chain[i]]
                        save(r.task_category, { chain })
                      }}
                      className="text-muted-foreground hover:text-foreground disabled:opacity-30"
                    >
                      <HugeiconsIcon icon={ArrowDown01Icon} className="size-4" />
                    </button>
                    <button
                      type="button"
                      aria-label={`Remove ${e.model}`}
                      onClick={() =>
                        save(r.task_category, { chain: r.chain.filter((_, j) => j !== i) })
                      }
                      className="text-muted-foreground hover:text-red-500"
                    >
                      <HugeiconsIcon icon={Delete02Icon} className="size-4" />
                    </button>
                  </span>
                </li>
              ))}
            </ol>
            <AddChainEntry
              providers={providers}
              onAdd={(entry) => save(r.task_category, { chain: [...r.chain, entry] })}
            />
          </div>
        )
      })}
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
    <div className="mt-3 flex flex-wrap items-center gap-2">
      <Select
        value={providerID}
        onValueChange={(id) => {
          setProviderID(id)
          const p = providers.find((x) => x.id === id)
          setManual((p?.models.length ?? 0) === 0)
          setModel(p?.default_model ?? '')
        }}
      >
        <SelectTrigger className="h-8 w-40" aria-label="Provider">
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
          className="h-8 w-56"
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
          <SelectTrigger className="h-8 w-56" aria-label="Model">
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
        size="sm"
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
  )
}
