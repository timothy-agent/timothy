import { ArrowDown01Icon, ArrowUp01Icon, Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import {
  createProvider,
  deleteProvider,
  getSettings,
  listProviders,
  listRoutes,
  patchBudget,
  patchProvider,
  patchRoute,
  patchSettings,
  providersHealth,
  testProvider,
  usageBudget,
} from '../api/client'
import type { AdminProvider, AdminRoute, ChainEntry, ProviderHealth, TestResult } from '../api/types'
import { Button } from '../components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../components/ui/dialog'
import { Input } from '../components/ui/input'

const tabs = ['Providers', 'Task allocation', 'Features'] as const

export function Settings() {
  const [tab, setTab] = useState<(typeof tabs)[number]>('Providers')
  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto max-w-4xl py-8">
        <h1 className="text-2xl font-semibold tracking-tight">Settings</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Providers, task allocation, and feature switches — changes serve immediately, no
          restarts.
        </p>
        <div className="mt-5 flex gap-1 border-b border-border">
          {tabs.map((t) => (
            <button
              key={t}
              type="button"
              onClick={() => setTab(t)}
              className={
                tab === t
                  ? 'border-b-2 border-blue-500 px-3 py-2 text-sm font-medium'
                  : 'px-3 py-2 text-sm text-muted-foreground hover:text-foreground'
              }
            >
              {t}
            </button>
          ))}
        </div>
        {tab === 'Providers' && <ProvidersTab />}
        {tab === 'Task allocation' && <RoutesTab />}
        {tab === 'Features' && <FeaturesTab />}
      </div>
    </div>
  )
}

// Toggle is a dependency-free switch that reads in both themes.
function Toggle({ on, onChange, label }: { on: boolean; onChange: (v: boolean) => void; label: string }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      aria-label={label}
      onClick={() => onChange(!on)}
      className={`relative h-6 w-10 rounded-full transition ${on ? 'bg-blue-600' : 'bg-zinc-300 dark:bg-zinc-700'}`}
    >
      <span
        className={`absolute top-0.5 size-5 rounded-full bg-white shadow transition-all ${on ? 'left-4.5' : 'left-0.5'}`}
      />
    </button>
  )
}

// --- Providers tab ---

function ProvidersTab() {
  const [providers, setProviders] = useState<AdminProvider[]>([])
  const [health, setHealth] = useState<Record<string, ProviderHealth>>({})
  const [error, setError] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<AdminProvider | null>(null)

  const refresh = useCallback(() => {
    Promise.all([listProviders(), providersHealth()])
      .then(([list, rows]) => {
        setProviders(list)
        setHealth(Object.fromEntries(rows.map((h) => [h.name, h])))
        setError(null)
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)))
  }, [])
  useEffect(refresh, [refresh])

  const remove = async () => {
    if (!confirmDelete) return
    try {
      await deleteProvider(confirmDelete.id)
      setConfirmDelete(null)
      refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setConfirmDelete(null)
    }
  }

  return (
    <div className="mt-6 space-y-4">
      {error && (
        <div className="rounded-xl border border-red-500/30 bg-red-500/5 p-3 text-sm text-red-600 dark:text-red-400">
          {error}
        </div>
      )}
      {providers.map((p) => (
        <ProviderCard
          key={p.id}
          provider={p}
          health={health[p.name]}
          onChanged={refresh}
          onDelete={() => setConfirmDelete(p)}
          onError={setError}
        />
      ))}
      {providers.length === 0 && !error && (
        <div className="rounded-xl border border-dashed p-10 text-center text-sm text-muted-foreground">
          No providers configured yet.
        </div>
      )}
      <AddProvider onAdded={refresh} onError={setError} />

      <Dialog open={confirmDelete !== null} onOpenChange={(o) => !o && setConfirmDelete(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {confirmDelete?.name}?</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            Removes the provider row and its models. Refused while an enabled route still points
            at it.
          </p>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmDelete(null)}>
              Cancel
            </Button>
            <Button variant="destructive" onClick={() => void remove()}>
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function ProviderCard({
  provider,
  health,
  onChanged,
  onDelete,
  onError,
}: {
  provider: AdminProvider
  health?: ProviderHealth
  onChanged: () => void
  onDelete: () => void
  onError: (msg: string) => void
}) {
  const [ref, setRef] = useState(provider.credential_ref)
  const [testing, setTesting] = useState(false)
  const [test, setTest] = useState<TestResult | null>(null)

  const toggle = (enabled: boolean) => {
    patchProvider(provider.id, { enabled }).then(onChanged, (err: unknown) =>
      onError(err instanceof Error ? err.message : String(err)),
    )
  }

  const saveRef = () => {
    if (ref === provider.credential_ref) return
    patchProvider(provider.id, { credential_ref: ref }).then(onChanged, (err: unknown) => {
      // Revert on rejection: a mistakenly pasted secret must not linger
      // in this field's state after the server refuses it.
      setRef(provider.credential_ref)
      onError(err instanceof Error ? err.message : String(err))
    })
  }

  const runTest = async () => {
    setTesting(true)
    setTest(null)
    try {
      setTest(await testProvider(provider.id))
    } catch (err) {
      setTest({ ok: false, latency_ms: 0, model: '', detail: err instanceof Error ? err.message : String(err) })
    } finally {
      setTesting(false)
    }
  }

  return (
    <div className="rounded-xl border border-border p-4">
      <div className="flex flex-wrap items-center gap-3">
        <span
          className={`size-2.5 rounded-full ${health?.healthy ? 'bg-emerald-500' : 'bg-red-500'}`}
          title={health?.healthy ? 'credential resolves' : 'credential missing'}
        />
        <span className="font-medium">{provider.name}</span>
        <span className="rounded bg-zinc-100 px-1.5 py-px text-[10px] font-medium uppercase text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400">
          {provider.kind}
        </span>
        <span className="text-xs text-muted-foreground">{provider.driver}</span>
        <div className="ml-auto flex items-center gap-3">
          <Button size="sm" variant="outline" disabled={testing} onClick={() => void runTest()}>
            {testing ? 'Testing…' : 'Test'}
          </Button>
          <Toggle on={provider.enabled} onChange={toggle} label={`${provider.name} enabled`} />
          <button
            type="button"
            aria-label={`Delete ${provider.name}`}
            onClick={onDelete}
            className="text-muted-foreground hover:text-red-500"
          >
            <HugeiconsIcon icon={Delete02Icon} className="size-4" />
          </button>
        </div>
      </div>

      {test && (
        <div
          className={`mt-3 rounded-lg border p-2.5 text-xs ${test.ok ? 'border-emerald-500/30 bg-emerald-500/5 text-emerald-700 dark:text-emerald-400' : 'border-red-500/30 bg-red-500/5 text-red-600 dark:text-red-400'}`}
        >
          {test.ok
            ? `OK — ${test.model} answered in ${test.latency_ms} ms`
            : `Failed after ${test.latency_ms} ms: ${test.detail}`}
        </div>
      )}

      <div className="mt-3 grid gap-3 sm:grid-cols-2">
        <label className="text-xs text-muted-foreground">
          Credential reference
          <div className="mt-1 flex gap-2">
            <Input
              value={ref}
              onChange={(e) => setRef(e.target.value)}
              onBlur={saveRef}
              placeholder="ENV_VAR_NAME or profile"
              className="h-8"
            />
          </div>
          <span className="mt-1 block">
            Name of the env var / Vault path / AWS profile that holds the key — never the key
            itself.
          </span>
        </label>
        <div className="text-xs text-muted-foreground">
          Models
          <ul className="mt-1 space-y-1">
            {provider.models.map((m) => (
              <li key={m.id} className="flex items-center gap-2 text-sm text-foreground">
                <span className="truncate">{m.id}</span>
                {provider.default_model === m.id && (
                  <span className="rounded bg-blue-500/10 px-1.5 py-px text-[10px] text-blue-600 dark:text-blue-400">
                    default
                  </span>
                )}
                {m.context_window != null && (
                  <span className="text-xs text-muted-foreground">
                    {Math.round(m.context_window / 1000)}k ctx
                  </span>
                )}
              </li>
            ))}
            {provider.models.length === 0 && <li className="text-xs">none declared</li>}
          </ul>
        </div>
      </div>
    </div>
  )
}

function AddProvider({ onAdded, onError }: { onAdded: () => void; onError: (m: string) => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [driver, setDriver] = useState('openaicompat')
  const [baseURL, setBaseURL] = useState('')
  const [ref, setRef] = useState('')

  const add = async () => {
    try {
      await createProvider({
        name,
        kind: 'api',
        driver,
        base_url: baseURL,
        credential_ref: ref,
        models: [],
        headers: {},
        enabled: false,
      })
      setOpen(false)
      setName('')
      setBaseURL('')
      setRef('')
      onAdded()
    } catch (err) {
      onError(err instanceof Error ? err.message : String(err))
    }
  }

  if (!open) {
    return (
      <Button variant="outline" onClick={() => setOpen(true)}>
        Add provider
      </Button>
    )
  }
  return (
    <div className="rounded-xl border border-border p-4">
      <h3 className="text-sm font-medium">New provider</h3>
      <div className="mt-3 grid gap-3 sm:grid-cols-2">
        <Input placeholder="name (unique)" value={name} onChange={(e) => setName(e.target.value)} />
        <select
          aria-label="Driver"
          value={driver}
          onChange={(e) => setDriver(e.target.value)}
          className="h-9 rounded-md border border-input bg-transparent px-3 text-sm"
        >
          <option value="openaicompat">openaicompat</option>
          <option value="anthropic">anthropic</option>
          <option value="bedrock">bedrock</option>
          <option value="cli" disabled>
            cli — driver available in a later phase
          </option>
        </select>
        <Input
          placeholder="base URL (bedrock: AWS region)"
          value={baseURL}
          onChange={(e) => setBaseURL(e.target.value)}
        />
        <Input
          placeholder="credential ref (name, never a key)"
          value={ref}
          onChange={(e) => setRef(e.target.value)}
        />
      </div>
      <div className="mt-3 flex gap-2">
        <Button size="sm" onClick={() => void add()} disabled={!name}>
          Create
        </Button>
        <Button size="sm" variant="outline" onClick={() => setOpen(false)}>
          Cancel
        </Button>
      </div>
    </div>
  )
}

// --- Task allocation tab ---

function RoutesTab() {
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
      .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)))
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
    patchRoute(category, patch).then(refresh, (err: unknown) =>
      setError(err instanceof Error ? err.message : String(err)),
    )
  }

  return (
    <div className="mt-6 space-y-4">
      {error && (
        <div className="rounded-xl border border-red-500/30 bg-red-500/5 p-3 text-sm text-red-600 dark:text-red-400">
          {error}
        </div>
      )}
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

function AddChainEntry({
  providers,
  onAdd,
}: {
  providers: AdminProvider[]
  onAdd: (e: ChainEntry) => void
}) {
  const [providerID, setProviderID] = useState('')
  const [model, setModel] = useState('')
  return (
    <div className="mt-3 flex flex-wrap items-center gap-2">
      <select
        aria-label="Provider"
        value={providerID}
        onChange={(e) => {
          setProviderID(e.target.value)
          const p = providers.find((x) => x.id === e.target.value)
          if (p?.default_model) setModel(p.default_model)
        }}
        className="h-8 rounded-md border border-input bg-transparent px-2 text-sm"
      >
        <option value="">provider…</option>
        {providers.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name}
          </option>
        ))}
      </select>
      <Input
        aria-label="Model"
        placeholder="model id"
        value={model}
        onChange={(e) => setModel(e.target.value)}
        className="h-8 w-56"
      />
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

// --- Features tab ---

const featureCopy: Record<string, { label: string; description: string }> = {
  tools_enabled: {
    label: 'Tool execution',
    description: 'Off: chat answers as plain completion — no shell, no web fetch, no tool calls.',
  },
  memory_extraction_enabled: {
    label: 'Memory extraction',
    description: 'Off: turns stop feeding the long-term memory queue. Retrieval keeps working.',
  },
  compaction_enabled: {
    label: 'Compaction',
    description: 'Off: sessions grow unbounded until re-enabled. Useful when debugging context.',
  },
  scheduler_enabled: {
    label: 'Scheduler',
    description: 'Stored now; gains a consumer when the agent harness lands.',
  },
}

function FeaturesTab() {
  const [flags, setFlags] = useState<Record<string, boolean> | null>(null)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(() => {
    getSettings()
      .then((s) => {
        setFlags(s)
        setError(null)
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)))
  }, [])
  useEffect(refresh, [refresh])

  const flip = (key: string, value: boolean) => {
    setFlags((f) => (f ? { ...f, [key]: value } : f)) // optimistic
    patchSettings({ [key]: value }).catch((err: unknown) => {
      setError(err instanceof Error ? err.message : String(err))
      refresh()
    })
  }

  return (
    <div className="mt-6 space-y-3">
      {error && (
        <div className="rounded-xl border border-red-500/30 bg-red-500/5 p-3 text-sm text-red-600 dark:text-red-400">
          {error}
        </div>
      )}
      {Object.entries(featureCopy).map(([key, copy]) => (
        <div key={key} className="flex items-center gap-4 rounded-xl border border-border p-4">
          <div className="min-w-0 flex-1">
            <div className="text-sm font-medium">{copy.label}</div>
            <p className="mt-0.5 text-xs text-muted-foreground">{copy.description}</p>
          </div>
          <Toggle
            on={flags?.[key] ?? true}
            onChange={(v) => flip(key, v)}
            label={copy.label}
          />
        </div>
      ))}
      <BudgetsCard />
    </div>
  )
}

// BudgetsCard edits the gateway's spend budgets. Empty field = no
// budget for that window; both keys always travel so clearing works.
function BudgetsCard() {
  const [day, setDay] = useState('')
  const [month, setMonth] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    usageBudget()
      .then((b) => {
        setDay(b.day.limit_usd != null ? String(b.day.limit_usd) : '')
        setMonth(b.month.limit_usd != null ? String(b.month.limit_usd) : '')
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)))
  }, [])

  // '' → null (clear), positive number → set, anything else → invalid.
  const parse = (v: string): number | null | undefined => {
    if (v.trim() === '') return null
    const n = Number(v)
    return Number.isFinite(n) && n > 0 ? n : undefined
  }

  const save = () => {
    setSaved(false)
    const d = parse(day)
    const m = parse(month)
    if (d === undefined || m === undefined) {
      setError('Budgets must be positive USD amounts (or empty for no budget).')
      return
    }
    patchBudget({ day: d, month: m })
      .then(() => {
        setError(null)
        setSaved(true)
      })
      .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)))
  }

  return (
    <div className="rounded-xl border border-border p-4">
      <div className="text-sm font-medium">Spend budgets</div>
      <p className="mt-0.5 text-xs text-muted-foreground">
        USD limits per UTC day and month. When spend reaches a limit the dashboard shows a
        banner; Prometheus gauges carry the same signal for external alerting. Requests are
        never blocked.
      </p>
      {error && (
        <div className="mt-3 rounded-lg border border-red-500/30 bg-red-500/5 p-2 text-xs text-red-600 dark:text-red-400">
          {error}
        </div>
      )}
      <div className="mt-3 flex flex-wrap items-end gap-3">
        <label className="grid gap-1 text-xs text-muted-foreground">
          Daily (USD)
          <Input
            value={day}
            onChange={(e) => setDay(e.target.value)}
            placeholder="none"
            inputMode="decimal"
            className="w-32"
            aria-label="Daily budget in USD"
          />
        </label>
        <label className="grid gap-1 text-xs text-muted-foreground">
          Monthly (USD)
          <Input
            value={month}
            onChange={(e) => setMonth(e.target.value)}
            placeholder="none"
            inputMode="decimal"
            className="w-32"
            aria-label="Monthly budget in USD"
          />
        </label>
        <Button onClick={save}>Save budgets</Button>
        {saved && <span className="text-xs text-muted-foreground">Saved.</span>}
      </div>
    </div>
  )
}
