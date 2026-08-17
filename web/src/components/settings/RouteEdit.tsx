import { ArrowLeft01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, Navigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { catalogModelsForProvider, listProviders, listRoutes, patchRoute } from '../../api/client'
import type { AdminProvider, AdminRoute, ChainEntry } from '../../api/types'
import { Button } from '../ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../ui/select'
import { catalogMatchForID, catalogRowID, configuredPrice, ModelInput, type ModelSuggestion, useCatalogSearch } from './ModelInput'
import { Pipeline, type PipelineEntry } from './pipeline/Pipeline'
import { reorder } from './pipeline/useReorderDrag'
import { matchPreset } from './presets'
import { ProviderMark } from './ProviderLogo'
import { Field, Toggle } from './shared'
import { errText } from './util'

const scoredStrategies = ['auto', 'price', 'latency']

export function RouteEdit() {
  const { name } = useParams()
  const [route, setRoute] = useState<AdminRoute | null | undefined>(undefined)
  const [providers, setProviders] = useState<AdminProvider[]>([])

  const refresh = useCallback(() => {
    Promise.all([listRoutes(), listProviders()])
      .then(([routes, p]) => {
        setRoute(routes.find((r) => r.name === name) ?? null)
        setProviders(p)
      })
      .catch((err: unknown) => toast.error('Could not load route', { description: errText(err) }))
  }, [name])
  useEffect(refresh, [refresh])

  if (route === null) return <Navigate to="/settings/routes" replace />
  if (route === undefined) return null

  const nameOf = (id: string) => providers.find((p) => p.id === id)?.name ?? id.slice(0, 8)
  const scored = scoredStrategies.includes(route.strategy)
  const serving = route.serving

  const save = (patch: { chain?: ChainEntry[]; strategy?: string; enabled?: boolean }) => {
    patchRoute(route.name, patch).then(refresh, (err: unknown) =>
      toast.error('Could not update route', { description: errText(err) }),
    )
  }

  // Display order: the router's resolved order for scored strategies
  // (what actually gets tried), written chain order otherwise — where
  // resolved lines up index-for-index because ordered routes never
  // re-sort. Chain entry references are preserved so edits map back.
  const displayEntries: PipelineEntry[] = (() => {
    if (scored && route.resolved) {
      const pool = [...route.chain]
      return route.resolved.map((s) => {
        const i = pool.findIndex(
          (c) => c.provider_id === s.provider_id && (c.model === s.model || c.model === ''),
        )
        const entry = i >= 0 ? pool.splice(i, 1)[0] : { provider_id: s.provider_id, model: s.model }
        return { entry, status: s }
      })
    }
    return route.chain.map((e, i) => ({ entry: e, status: route.resolved?.[i] }))
  })()

  // Reorder positions are display positions; drag and arrows are only
  // live for ordered routes, where display order IS chain order.
  const moveEntry = (from: number, to: number) => {
    if (to < 0 || to >= route.chain.length || from === to) return
    save({ chain: reorder(route.chain, from, to) })
  }
  const removeEntry = (displayIndex: number) => {
    const target = displayEntries[displayIndex].entry
    const i = route.chain.indexOf(target)
    const chain =
      i >= 0
        ? route.chain.filter((_, j) => j !== i)
        : route.chain.filter((c) => !(c.provider_id === target.provider_id && c.model === target.model))
    save({ chain })
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

      <div className="space-y-4">
        <div className="flex items-center gap-3">
          <h2 className="text-sm font-semibold">Chain</h2>
          {scored ? (
            <span className="rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
              auto-sorted by score
            </span>
          ) : (
            <span className="text-xs text-muted-foreground">drag cards to set priority</span>
          )}
        </div>
        <Pipeline
          entries={displayEntries}
          scored={scored}
          serving={serving}
          providers={providers}
          onReorder={moveEntry}
          onRemove={removeEntry}
        />
        <AddChainEntry providers={providers} onAdd={(entry) => save({ chain: [...route.chain, entry] })} />
      </div>
    </div>
  )
}

function AddChainEntry({
  providers,
  onAdd,
}: {
  providers: AdminProvider[]
  onAdd: (e: ChainEntry) => void
}) {
  const [providerID, setProviderID] = useState('')
  const [model, setModel] = useState('')

  const selected = providers.find((x) => x.id === providerID)

  // Live type-ahead over the selected provider's candidate catalog
  // rows, keyed on the typed model id.
  const catalogSearch = useCallback(
    (q: string) => (providerID ? catalogModelsForProvider(providerID, q) : Promise.resolve([])),
    [providerID],
  )
  const catalogModels = useCatalogSearch(model, catalogSearch)

  // Declared models on the selected provider first, then live catalog
  // rows not already declared — same shape ProviderAdd/ProviderEdit
  // feed ModelInput, so price labels render the same way everywhere.
  const suggestions: ModelSuggestion[] = useMemo(() => {
    if (!selected) return []
    const seen = new Map<string, ModelSuggestion>()
    for (const m of selected.models) {
      const catalogMatch = catalogMatchForID(m.id, catalogModels)
      const price = configuredPrice(m.prices) ?? {
        input_per_mtok: catalogMatch?.input_per_mtok,
        output_per_mtok: catalogMatch?.output_per_mtok,
      }
      seen.set(m.id, { id: m.id, ...price })
    }
    for (const m of catalogModels) {
      const id = catalogRowID(m)
      if (!seen.has(id)) {
        seen.set(id, {
          id,
          input_per_mtok: m.input_per_mtok,
          output_per_mtok: m.output_per_mtok,
        })
      }
    }
    return [...seen.values()]
  }, [selected, catalogModels])

  return (
    <Field label="Add a provider to this chain">
      <div className="mt-1.5 flex flex-wrap items-center gap-2">
        <Select
          value={providerID}
          onValueChange={(id) => {
            setProviderID(id)
            const p = providers.find((x) => x.id === id)
            setModel(p?.default_model ?? '')
          }}
        >
          <SelectTrigger className="h-10 w-44" aria-label="Provider">
            <SelectValue placeholder="provider…" />
          </SelectTrigger>
          <SelectContent>
            {providers.map((p) => (
              <SelectItem key={p.id} value={p.id}>
                <ProviderMark preset={matchPreset(p)} />
                {p.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <ModelInput
          value={model}
          onChange={setModel}
          suggestions={suggestions}
          placeholder="model id"
          className="h-10 w-56"
          ariaLabel="Model"
        />
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
