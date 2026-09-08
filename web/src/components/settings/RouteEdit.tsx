import { useCallback, useEffect, useMemo, useState } from 'react'
import { Navigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { catalogModelsForProvider, listProviders, listRoutes, patchRoute } from '../../api/client'
import type { AdminProvider, AdminRoute, ChainEntry } from '../../api/types'
import { Field, Form, FormActions } from '../timothy/field'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { Alert, AlertDescription } from '../ui/alert'
import { Button } from '../ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../ui/select'
import { Switch } from '../ui/switch'
import { catalogRowID, ModelPicker, type ModelSuggestion, useCatalogSearch } from './ModelPicker'
import { Pipeline, type PipelineEntry } from './pipeline/Pipeline'
import { reorder } from './pipeline/useReorderDrag'
import { matchPreset } from './presets'
import { ProviderMark } from './ProviderLogo'
import { ServingLine } from './ServingLine'
import { settingsArea } from './settingsAreas'
import { useStagedForm } from './useStagedForm'
import { errText } from './util'

const area = settingsArea('routes')
const scoredStrategies = ['auto', 'price', 'latency']

interface StagedRoute {
  strategy: string
  enabled: boolean
  chain: ChainEntry[]
}

function baselineFrom(route: AdminRoute): StagedRoute {
  return {
    strategy: route.strategy || 'ordered',
    enabled: route.enabled,
    chain: route.chain,
  }
}

export function RouteEdit() {
  const { name } = useParams()
  const [route, setRoute] = useState<AdminRoute | null | undefined>(undefined)
  const [providers, setProviders] = useState<AdminProvider[]>([])

  const refresh = useCallback(() => {
    return Promise.all([listRoutes(), listProviders()])
      .then(([routes, p]) => {
        const found = routes.find((r) => r.name === name) ?? null
        setRoute(found)
        setProviders(p)
        return found
      })
      .catch((err: unknown) => {
        toast.error('Could not load route', { description: errText(err) })
        return undefined
      })
  }, [name])
  useEffect(() => {
    void refresh()
  }, [refresh])

  if (route === null) return <Navigate to="/settings/routes" replace />
  if (route === undefined) return null

  return <RouteEditForm key={route.name} initialRoute={route} providers={providers} refresh={refresh} />
}

function RouteEditForm({
  initialRoute,
  providers,
  refresh,
}: {
  initialRoute: AdminRoute
  providers: AdminProvider[]
  refresh: () => Promise<AdminRoute | null | undefined>
}) {
  const [route, setRouteState] = useState(initialRoute)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  const staged = useStagedForm<StagedRoute>(baselineFrom(initialRoute))

  const doRefresh = useCallback(async () => {
    const refetched = await refresh()
    if (refetched) setRouteState(refetched)
    return refetched
  }, [refresh])

  const save = useCallback(async () => {
    setSaving(true)
    setSaveError(null)
    try {
      await patchRoute(route.name, {
        strategy: staged.values.strategy,
        enabled: staged.values.enabled,
        chain: staged.values.chain,
      })
      toast.success('Route saved')
      const refetched = await doRefresh()
      if (refetched) staged.rebase(baselineFrom(refetched))
    } catch (err) {
      setSaveError(errText(err))
    } finally {
      setSaving(false)
    }
  }, [route.name, doRefresh, staged])

  const nameOf = (id: string) => providers.find((p) => p.id === id)?.name ?? id.slice(0, 8)
  const scored = scoredStrategies.includes(staged.values.strategy)
  const serving = route.serving

  // Display order: the router's resolved order for scored strategies
  // (what actually gets tried), staged chain order otherwise, where
  // resolved lines up index-for-index because ordered routes never
  // re-sort. Chain entry references are preserved so edits map back.
  const displayEntries: PipelineEntry[] = useMemo(() => {
    if (scored && route.resolved) {
      const pool = [...staged.values.chain]
      return route.resolved.map((s) => {
        const i = pool.findIndex(
          (c) => c.provider_id === s.provider_id && (c.model === s.model || c.model === ''),
        )
        const entry = i >= 0 ? pool.splice(i, 1)[0] : { provider_id: s.provider_id, model: s.model }
        return { entry, status: s }
      })
    }
    return staged.values.chain.map((e, i) => ({ entry: e, status: route.resolved?.[i] }))
  }, [scored, route.resolved, staged.values.chain])

  // Reorder positions are display positions; drag and arrows are only
  // live for ordered routes, where display order IS chain order. Both
  // mutate staged state only (contract 10.7); nothing is written here.
  const moveEntry = (from: number, to: number) => {
    if (to < 0 || to >= staged.values.chain.length || from === to) return
    staged.setField('chain', reorder(staged.values.chain, from, to))
  }
  const removeEntry = (displayIndex: number) => {
    const target = displayEntries[displayIndex].entry
    const i = staged.values.chain.indexOf(target)
    const chain =
      i >= 0
        ? staged.values.chain.filter((_, j) => j !== i)
        : staged.values.chain.filter((c) => !(c.provider_id === target.provider_id && c.model === target.model))
    staged.setField('chain', chain)
  }
  const addEntry = (entry: ChainEntry) => {
    staged.setField('chain', [...staged.values.chain, entry])
  }

  return (
    <PageShell width="full">
      <PageHeader
        title={route.name}
        breadcrumbs={[
          { label: 'Settings', href: '/settings' },
          { label: area.label, href: '/settings/routes' },
          { label: route.name },
        ]}
      >
        <ServingLine route={{ ...route, enabled: staged.values.enabled }} nameOf={nameOf} />
      </PageHeader>

      <Form
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <div className="max-w-2xl space-y-5">
          <div className="flex flex-wrap items-end gap-4">
            <Field label="Strategy" htmlFor="route-strategy">
              {(props) => (
                <Select value={staged.values.strategy} onValueChange={(v) => staged.setField('strategy', v)}>
                  <SelectTrigger id={props.id} aria-label={`${route.name} strategy`} className="w-36">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="ordered">Ordered</SelectItem>
                    <SelectItem value="auto">Auto</SelectItem>
                    <SelectItem value="price">Cheapest</SelectItem>
                    <SelectItem value="latency">Fastest</SelectItem>
                  </SelectContent>
                </Select>
              )}
            </Field>
            <div className="flex items-center gap-3 pb-2">
              <Switch
                checked={staged.values.enabled}
                onCheckedChange={(v) => staged.setField('enabled', v)}
                aria-label={`${route.name} route enabled`}
              />
              <span className="text-sm text-muted-foreground">Enabled</span>
            </div>
          </div>
        </div>

        <div className="w-full space-y-4">
          <div className="flex items-center gap-3">
            <h2 className="text-sm font-semibold">Chain</h2>
            {scored ? (
              <span className="rounded-md bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
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
          <div className="max-w-2xl">
            <AddChainEntry providers={providers} onAdd={addEntry} />
          </div>
        </div>

        {saveError && (
          <Alert tone="destructive">
            <AlertDescription className="flex flex-wrap items-center justify-between gap-3">
              <span>Could not save route changes: {saveError}</span>
              <Button type="button" size="sm" variant="outline" onClick={() => void save()}>
                Retry
              </Button>
            </AlertDescription>
          </Alert>
        )}

        <FormActions note={staged.dirty ? 'Unsaved changes' : undefined}>
          <Button type="button" variant="outline" disabled={saving} onClick={staged.reset}>
            Cancel
          </Button>
          <Button type="submit" disabled={!staged.dirty || saving}>
            Save
          </Button>
        </FormActions>
      </Form>
    </PageShell>
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

  // Live catalog rows for the selected provider, same shape
  // ProviderAdd/ProviderEdit feed ModelInput, so price labels render
  // the same way everywhere.
  const suggestions: ModelSuggestion[] = useMemo(() => {
    if (!selected) return []
    return catalogModels.map((m) => ({
      id: catalogRowID(m),
      input_per_mtok: m.input_per_mtok,
      output_per_mtok: m.output_per_mtok,
    }))
  }, [selected, catalogModels])

  return (
    <Field label="Add a provider to this chain">
      {() => (
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
          <ModelPicker
            value={model}
            onChange={setModel}
            suggestions={suggestions}
            placeholder="model id"
            className="h-10 w-56"
            ariaLabel="Model"
          />
          <Button
            type="button"
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
      )}
    </Field>
  )
}
