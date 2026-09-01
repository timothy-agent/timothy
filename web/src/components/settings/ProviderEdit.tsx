import { ArrowLeft01Icon, Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import {
  availableModels,
  catalogModelsForProvider,
  deleteProvider,
  deleteSecret,
  listProviders,
  patchProvider,
  secretStatus,
  setSecret,
  testProvider,
} from '../../api/client'
import type { AdminProvider, TestResult } from '../../api/types'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { catalogRowID, ModelInput, priceLabel, type ModelSuggestion, useCatalogSearch } from './ModelInput'
import { bedrockRegions, matchPreset } from './presets'
import { ProviderLogo } from './ProviderLogo'
import { Field, Toggle } from './shared'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'
import { backendLabel, errText, isTimothyAuthDetail, isTimothyAuthError, probeFailureText, responsesSuffix, secretDestination, stripPaste } from './util'

// ProviderEdit is a provider's own page for the controls too heavy for
// its summary card: rotating the stored key, and declaring which
// models it serves.
export function ProviderEdit() {
  const { id } = useParams()
  const navigate = useNavigate()
  const defaultBackend = useDefaultSecretBackend()

  const [provider, setProvider] = useState<AdminProvider | null | undefined>(undefined)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [name, setName] = useState('')

  const refresh = useCallback(() => {
    listProviders()
      .then((list) => setProvider(list.find((p) => p.id === id) ?? null))
      .catch((err: unknown) => toast.error('Could not load provider', { description: errText(err) }))
  }, [id])
  useEffect(refresh, [refresh])
  useEffect(() => {
    if (provider) setName(provider.name)
  }, [provider])

  const saveName = () => {
    if (!provider) return
    const trimmed = name.trim()
    if (!trimmed || trimmed === provider.name) {
      setName(provider.name)
      return
    }
    patchProvider(provider.id, { name: trimmed }).then(refresh, (err: unknown) => {
      setName(provider.name)
      toast.error('Could not rename provider', { description: errText(err) })
    })
  }

  const remove = async () => {
    if (!provider) return
    try {
      await deleteProvider(provider.id)
      toast.success('Provider removed', { description: `${provider.name} no longer routes any traffic.` })
      navigate('/settings/providers')
    } catch (err) {
      toast.error('Could not remove provider', { description: errText(err) })
      setConfirmDelete(false)
    }
  }

  if (provider === null) return <Navigate to="/settings/providers" replace />
  if (provider === undefined) return null

  return (
    <div className="mt-6 w-full space-y-6">
      <Link
        to="/settings/providers"
        className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground transition hover:text-foreground"
      >
        <HugeiconsIcon icon={ArrowLeft01Icon} className="size-4" />
        Providers
      </Link>

      <div className="flex items-center gap-4 border-b border-border pb-6">
        <ProviderLogo preset={matchPreset(provider)} className="size-12" />
        <div className="min-w-0 flex-1">
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            onBlur={saveName}
            onKeyDown={(e) => {
              if (e.key === 'Enter') e.currentTarget.blur()
            }}
            className="h-9 max-w-sm truncate border-transparent bg-transparent px-0 text-xl font-semibold tracking-tight shadow-none hover:border-border focus-visible:border-border focus-visible:bg-background focus-visible:px-3"
            aria-label="Provider name"
          />
          <p className="text-sm text-muted-foreground">driver: {provider.driver}</p>
        </div>
        <Button variant="destructive" onClick={() => setConfirmDelete(true)}>
          <HugeiconsIcon icon={Delete02Icon} />
          Delete
        </Button>
      </div>

      <div className="grid max-w-3xl gap-8">
        {provider.driver === 'bedrock' ? (
          <CredentialSection provider={provider} onChanged={refresh} bedrock />
        ) : (
          <CredentialSection
            provider={provider}
            onChanged={refresh}
            defaultBackend={defaultBackend}
            isCli={provider.kind === 'cli'}
          />
        )}
        {provider.driver === 'bedrock' && <RegionSection provider={provider} onChanged={refresh} />}
        {provider.kind !== 'cli' && (
          <CatalogProviderSection provider={provider} onChanged={refresh} />
        )}
        {provider.driver === 'openaicompat' && (
          <ReasoningSection provider={provider} onChanged={refresh} />
        )}
        {provider.kind === 'cli' ? (
          <CliModelsSection provider={provider} onChanged={refresh} />
        ) : (
          <>
            <DefaultModelSection provider={provider} onChanged={refresh} />
            <ProviderCatalogSection provider={provider} />
          </>
        )}
      </div>

      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {provider.name}?</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            Removes the provider row and its models. Refused while an enabled route still points
            at it.
          </p>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmDelete(false)}>
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

function CredentialSection({
  provider,
  onChanged,
  defaultBackend,
  bedrock,
  isCli,
}: {
  provider: AdminProvider
  onChanged: () => void
  defaultBackend?: string
  bedrock?: boolean
  isCli?: boolean
}) {
  const [ref, setRef] = useState(provider.credential_ref)
  const [configured, setConfigured] = useState(false)
  const [storedBackend, setStoredBackend] = useState('')
  const [secretValue, setSecretValue] = useState('')
  const [accessKeyId, setAccessKeyId] = useState('')
  const [secretAccessKey, setSecretAccessKey] = useState('')
  const [savingSecret, setSavingSecret] = useState(false)
  const [test, setTest] = useState<TestResult | null>(null)
  const [testing, setTesting] = useState(false)

  const refreshSecretStatus = useCallback(() => {
    if (!provider.credential_ref) {
      setConfigured(false)
      setStoredBackend('')
      return
    }
    secretStatus(provider.credential_ref).then(
      (s) => {
        setConfigured(s.configured)
        setStoredBackend(s.backend)
      },
      () => {
        setConfigured(false)
        setStoredBackend('')
      },
    )
  }, [provider.credential_ref])
  useEffect(refreshSecretStatus, [refreshSecretStatus])

  const saveRef = () => {
    if (ref === provider.credential_ref) return
    patchProvider(provider.id, { credential_ref: ref }).then(onChanged, (err: unknown) => {
      setRef(provider.credential_ref)
      toast.error('Could not update credential reference', { description: errText(err) })
    })
  }

  const bedrockKeyJSON = () =>
    JSON.stringify({
      access_key_id: stripPaste(accessKeyId.trim()),
      secret_access_key: stripPaste(secretAccessKey.trim()),
    })

  const saveSecretValue = async () => {
    if (!ref) return
    if (bedrock) {
      if (!accessKeyId.trim() || !secretAccessKey.trim()) return
    } else if (!secretValue) {
      return
    }
    setSavingSecret(true)
    try {
      await setSecret(ref, bedrock ? bedrockKeyJSON() : stripPaste(secretValue))
      setSecretValue('')
      setAccessKeyId('')
      setSecretAccessKey('')
      refreshSecretStatus()
      onChanged()
      toast.success('Key saved')
    } catch (err) {
      toast.error('Could not save key', { description: errText(err) })
    } finally {
      setSavingSecret(false)
    }
  }

  const clearSecretValue = async () => {
    if (!ref) return
    setSavingSecret(true)
    try {
      await deleteSecret(ref)
      refreshSecretStatus()
      onChanged()
    } catch (err) {
      toast.error('Could not clear key', { description: errText(err) })
    } finally {
      setSavingSecret(false)
    }
  }

  const runTest = async () => {
    setTesting(true)
    setTest(null)
    try {
      const res = await testProvider(provider.id)
      if (!res.ok && isTimothyAuthDetail(res.detail)) {
        setTest(null)
        return
      }
      setTest(res)
    } catch (err) {
      if (isTimothyAuthError(err)) {
        setTest(null)
        return
      }
      setTest({ ok: false, latency_ms: 0, model: '', detail: errText(err) })
    } finally {
      setTesting(false)
    }
  }

  return (
    <section className="space-y-4">
      <h2 className="text-sm font-semibold">
        {bedrock ? 'AWS credentials' : isCli ? 'Subscription token' : 'API key'}
      </h2>

      {isCli ? (
        <p
          className="rounded-xl border border-border bg-muted/40 p-4 text-sm text-muted-foreground"
          title="No chat driver exists for a subscription provider (D-051); health instead reflects the last delegated harness run."
        >
          Health reflects the last harness run, not a live probe — subscription auth has no chat
          endpoint to test.
        </p>
      ) : (
        <div
          className={
            'flex flex-wrap items-center gap-3 rounded-xl border p-4 text-sm ' +
            (test?.ok
              ? 'border-good/30 bg-good-soft text-good'
              : test && !test.ok
                ? 'border-destructive/30 bg-destructive/5 text-destructive'
                : 'border-border bg-muted/40 text-muted-foreground')
          }
        >
          <span className="min-w-0 flex-1 font-medium">
            {testing
              ? 'Testing connection…'
              : test?.ok
                ? `OK, ${test.model} answered in ${test.latency_ms} ms.${responsesSuffix(test)}`
                : test && !test.ok
                  ? probeFailureText(test)
                  : 'Not tested yet.'}
          </span>
          <Button size="sm" variant="test" disabled={testing} onClick={() => void runTest()}>
            {testing ? 'Testing…' : 'Test connection'}
          </Button>
        </div>
      )}

      <div className="space-y-2">
        <div className="flex items-center gap-2">
          <span
            className={`rounded px-2 py-0.5 text-xs font-semibold uppercase ${
              configured ? 'bg-good-soft text-good' : 'bg-warning-soft text-warning'
            }`}
          >
            {configured ? `stored · ${backendLabel(storedBackend)}` : 'not set'}
          </span>
          {configured && (
            <button
              type="button"
              disabled={savingSecret}
              onClick={() => void clearSecretValue()}
              className="text-sm text-muted-foreground underline-offset-2 hover:text-destructive hover:underline"
            >
              clear
            </button>
          )}
        </div>
        {bedrock ? (
          <>
            <div className="grid gap-3 sm:grid-cols-2">
              <Input
                type="password"
                value={accessKeyId}
                onChange={(e) => setAccessKeyId(e.target.value)}
                placeholder="AKIA…"
                className="h-10"
                autoComplete="off"
                aria-label="Access Key ID"
              />
              <Input
                type="password"
                value={secretAccessKey}
                onChange={(e) => setSecretAccessKey(e.target.value)}
                placeholder={configured ? 'paste new secret key to rotate' : 'wJalrXUtnFEMI/K7MDEN...'}
                className="h-10"
                autoComplete="off"
                aria-label="Secret Access Key"
              />
            </div>
            <Button
              variant="outline"
              disabled={savingSecret || !accessKeyId.trim() || !secretAccessKey.trim() || !ref}
              onClick={() => void saveSecretValue()}
            >
              Save
            </Button>
          </>
        ) : (
          <div className="flex gap-2">
            <Input
              type="password"
              value={secretValue}
              onChange={(e) => setSecretValue(e.target.value)}
              placeholder={configured ? 'paste new key to rotate' : 'paste key'}
              className="h-10"
              autoComplete="off"
            />
            <Button variant="outline" disabled={savingSecret || !secretValue || !ref} onClick={() => void saveSecretValue()}>
              Save
            </Button>
          </div>
        )}
        {!bedrock && <p className="text-sm text-muted-foreground">{secretDestination(defaultBackend ?? 'db', ref)}</p>}
        <details className="pt-1">
          <summary className="cursor-pointer text-sm font-medium text-muted-foreground transition hover:text-foreground">
            Advanced: reference name
          </summary>
          <Input
            value={ref}
            onChange={(e) => setRef(e.target.value)}
            onBlur={saveRef}
            placeholder="name (e.g. ANTHROPIC_API_KEY)"
            className="mt-1.5 h-10"
          />
          <p className="mt-1.5 text-sm text-muted-foreground">
            Storage name for this provider’s key, never the key itself. Change it only to
            share or repoint an already-stored secret.
          </p>
        </details>
      </div>
    </section>
  )
}

// ReasoningSection lets an openaicompat provider force reasoning off
// (D-040) — e.g. Ollama's /v1/chat/completions endpoint silently
// ignores the native "think": false flag but honors
// reasoning_effort: "none". Disabled is the only override exposed;
// toggling on omits the key entirely rather than writing a default.
// The same section also holds the request timeout override (D-041), a
// Go duration string (e.g. "20m") for slow backends — a CPU-only
// remote Ollama, say — that need more than the driver's 5m default.
function ReasoningSection({ provider, onChanged }: { provider: AdminProvider; onChanged: () => void }) {
  const [saving, setSaving] = useState(false)
  const [timeout, setTimeoutValue] = useState(provider.options?.request_timeout ?? '')
  const [timeoutSaving, setTimeoutSaving] = useState(false)
  const disabled = provider.options?.reasoning_effort === 'none'

  // provider is refetched (via onChanged/refresh) whenever ANY field on
  // this page saves, including ones outside this section — without this
  // sync the box silently keeps showing what you typed even after a
  // sibling edit reloads the provider out from under it.
  useEffect(() => {
    setTimeoutValue(provider.options?.request_timeout ?? '')
  }, [provider.options?.request_timeout])

  const toggle = async (on: boolean) => {
    setSaving(true)
    try {
      const { reasoning_effort: _reasoningEffort, ...rest } = provider.options ?? {}
      await patchProvider(provider.id, {
        options: on ? { ...rest, reasoning_effort: 'none' } : rest,
      })
      onChanged()
    } catch (err) {
      toast.error('Could not update reasoning setting', { description: errText(err) })
    } finally {
      setSaving(false)
    }
  }

  const saveTimeout = async () => {
    const trimmed = timeout.trim()
    if (trimmed === (provider.options?.request_timeout ?? '')) return
    const { request_timeout: _requestTimeout, ...rest } = provider.options ?? {}
    setTimeoutSaving(true)
    try {
      await patchProvider(provider.id, {
        options: trimmed ? { ...rest, request_timeout: trimmed } : rest,
      })
      onChanged()
    } catch (err) {
      setTimeoutValue(provider.options?.request_timeout ?? '')
      toast.error('Could not update request timeout', { description: errText(err) })
    } finally {
      setTimeoutSaving(false)
    }
  }

  return (
    <section className="space-y-4">
      <h2 className="text-sm font-semibold">Reasoning</h2>
      <label className="flex items-center gap-3 text-sm">
        <Toggle on={disabled} onChange={(v) => void toggle(v)} label="Disable reasoning" />
        <span className="text-muted-foreground">
          {saving ? 'Saving…' : 'Disable reasoning ("thinking") for every request to this provider.'}
        </span>
      </label>
      <Field
        label="Request timeout"
        hint={
          timeoutSaving
            ? 'Saving…'
            : 'Go duration, e.g. "20m", empty uses the default. Enter or click away to save.'
        }
      >
        <Input
          value={timeout}
          onChange={(e) => setTimeoutValue(e.target.value)}
          onBlur={() => void saveTimeout()}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.currentTarget.blur()
            }
          }}
          disabled={timeoutSaving}
          placeholder="5m"
          className="mt-1.5 block h-10 max-w-40"
        />
      </Field>
    </section>
  )
}

// RegionSection lets a bedrock provider pick its AWS region
// (options.region, D-048) — defaults to us-east-1 when unset, same
// default the gateway driver applies. Overridden per-key by the secret
// JSON's own "region" field (D-047), which this dropdown never touches.
function RegionSection({ provider, onChanged }: { provider: AdminProvider; onChanged: () => void }) {
  const [saving, setSaving] = useState(false)
  const region = provider.options?.region ?? 'us-east-1'

  const save = async (value: string) => {
    if (value === region) return
    setSaving(true)
    try {
      await patchProvider(provider.id, {
        options: { ...provider.options, region: value },
      })
      onChanged()
    } catch (err) {
      toast.error('Could not update region', { description: errText(err) })
    } finally {
      setSaving(false)
    }
  }

  return (
    <section className="space-y-4">
      <h2 className="text-sm font-semibold">Region</h2>
      <Field label="AWS region" hint={saving ? 'Saving…' : undefined}>
        <Select value={region} onValueChange={(v) => void save(v)}>
          <SelectTrigger className="mt-1.5 h-10 w-full max-w-72">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {bedrockRegions.map((r) => (
              <SelectItem key={r.value} value={r.value}>
                {r.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
    </section>
  )
}

// CatalogProviderSection lets an operator set options.litellm_provider
// — an explicit override of which catalog section (the synced
// LiteLLM data's own litellm_provider field, e.g. "xai", "zai") this
// provider's models are matched and priced against. Unset defers to
// the existing driver/host heuristic (catalog.CandidateProviders);
// most providers never need this, but it's the escape hatch for a
// host the heuristic doesn't recognize.
function CatalogProviderSection({ provider, onChanged }: { provider: AdminProvider; onChanged: () => void }) {
  const [value, setValue] = useState(provider.options?.litellm_provider ?? '')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    setValue(provider.options?.litellm_provider ?? '')
  }, [provider.options?.litellm_provider])

  const save = async () => {
    const trimmed = value.trim()
    if (trimmed === (provider.options?.litellm_provider ?? '')) return
    const { litellm_provider: _litellmProvider, ...rest } = provider.options ?? {}
    setSaving(true)
    try {
      await patchProvider(provider.id, {
        options: trimmed ? { ...rest, litellm_provider: trimmed } : rest,
      })
      onChanged()
    } catch (err) {
      setValue(provider.options?.litellm_provider ?? '')
      toast.error('Could not update catalog provider', { description: errText(err) })
    } finally {
      setSaving(false)
    }
  }

  return (
    <section className="space-y-4">
      <h2 className="text-sm font-semibold">Catalog provider</h2>
      <Field
        label="LiteLLM provider"
        hint={
          saving
            ? 'Saving…'
            : 'Which LiteLLM provider section this provider\'s models are priced under. Empty infers it from driver/base URL.'
        }
      >
        <Input
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onBlur={() => void save()}
          onKeyDown={(e) => {
            if (e.key === 'Enter') e.currentTarget.blur()
          }}
          disabled={saving}
          placeholder="e.g. xai, zai"
          className="mt-1.5 block h-10 max-w-40"
        />
      </Field>
    </section>
  )
}

// cliModelAliases are the Claude Code CLI's own model aliases (D-051)
// — a kind='cli' row has no chat driver to enumerate models against,
// so these are offered as suggestions rather than an editable
// declared-models list. 'fable' verified as a recognized alias in the
// sandbox image (unknown aliases trip a CLI warning, fable doesn't).
const cliModelAliases = ['fable', 'sonnet', 'opus', 'haiku']

// CliModelsSection replaces ModelsSection for kind='cli' providers:
// there's no provider API to list or declare models against, so this
// is just a picker (aliases pinned first, then live Anthropic catalog
// ids — the gateway maps a claude-cli row to the "anthropic" catalog
// provider) to set default_model, the model the CLI runs.
function CliModelsSection({ provider, onChanged }: { provider: AdminProvider; onChanged: () => void }) {
  const [defaultModel, setDefaultModel] = useState(provider.default_model)
  const [saving, setSaving] = useState(false)
  // Cursor has its own model slug namespace (not the Claude Code CLI's
  // aliases, and not filed under any catalog provider), so its picker
  // gets no alias pinning and no catalog-backed suggestions. Instead it
  // fetches Cursor's own live model list (below).
  const isCursor = provider.driver === 'cursor-cli'

  const search = useCallback((q: string) => catalogModelsForProvider(provider.id, q), [provider.id])
  const catalogModels = useCatalogSearch(defaultModel, search)

  // cursorModels is Cursor's own live list (GET .../providers/:id/models,
  // gateway-cached ~5min). Fetched once per provider id; failures or an
  // empty list just fall back to free text, no error toast, since this is
  // an advisory suggestion list, not a required lookup.
  const [cursorModels, setCursorModels] = useState<ModelSuggestion[]>([])
  useEffect(() => {
    if (!isCursor) return
    let cancelled = false
    availableModels(provider.id).then(
      (models) => {
        if (!cancelled) {
          setCursorModels(models.map((m) => ({ id: m.id, name: m.display_name })))
        }
      },
      () => {
        if (!cancelled) setCursorModels([])
      },
    )
    return () => {
      cancelled = true
    }
  }, [isCursor, provider.id])

  useEffect(() => {
    setDefaultModel(provider.default_model)
  }, [provider.default_model])

  const suggestions: ModelSuggestion[] = useMemo(() => {
    if (isCursor) return cursorModels
    const seen = new Map<string, ModelSuggestion>(cliModelAliases.map((a) => [a, { id: a }]))
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
  }, [catalogModels, isCursor, cursorModels])

  const saveDefaultModel = async (v: string) => {
    const trimmed = v.trim()
    if (trimmed === provider.default_model) return
    setSaving(true)
    try {
      await patchProvider(provider.id, { default_model: trimmed })
      onChanged()
    } catch (err) {
      setDefaultModel(provider.default_model)
      toast.error('Could not update default model', { description: errText(err) })
    } finally {
      setSaving(false)
    }
  }

  return (
    <section className="space-y-4">
      <h2 className="text-sm font-semibold">Models</h2>
      <p className="text-sm text-muted-foreground">
        {isCursor
          ? 'Subscription auth has no declared model list. This sets the default model the CLI runs.'
          : "Subscription auth uses the Claude Code CLI's own model aliases, not a declared list. This sets the default model the CLI runs."}
      </p>
      <Field
        label="Default model"
        hint={saving ? 'Saving…' : isCursor ? undefined : 'An alias, or a full Anthropic model id.'}
      >
        <ModelInput
          value={defaultModel}
          onChange={setDefaultModel}
          onCommit={(v) => {
            setDefaultModel(v)
            void saveDefaultModel(v)
          }}
          suggestions={suggestions}
          placeholder={isCursor ? 'composer-2.5' : 'sonnet'}
          className="mt-1.5 h-10"
        />
      </Field>
    </section>
  )
}

// DefaultModelSection sets a kind='api' provider's default_model — the
// model a chain entry with no model of its own falls back to
// (router.Resolve). Auto-seeded at creation from the cheapest capable
// catalog model, stays editable here since the catalog can drift
// (a model deprecated upstream) faster than an operator would think to
// re-create the provider.
function DefaultModelSection({ provider, onChanged }: { provider: AdminProvider; onChanged: () => void }) {
  const [defaultModel, setDefaultModel] = useState(provider.default_model)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    setDefaultModel(provider.default_model)
  }, [provider.default_model])

  const search = useCallback((q: string) => catalogModelsForProvider(provider.id, q), [provider.id])
  const catalogModels = useCatalogSearch(defaultModel, search)

  const suggestions: ModelSuggestion[] = useMemo(
    () =>
      catalogModels.map((m) => ({
        id: catalogRowID(m),
        input_per_mtok: m.input_per_mtok,
        output_per_mtok: m.output_per_mtok,
      })),
    [catalogModels],
  )

  const save = async (v: string) => {
    const trimmed = v.trim()
    if (trimmed === provider.default_model) return
    setSaving(true)
    try {
      await patchProvider(provider.id, { default_model: trimmed })
      onChanged()
    } catch (err) {
      setDefaultModel(provider.default_model)
      toast.error('Could not update default model', { description: errText(err) })
    } finally {
      setSaving(false)
    }
  }

  return (
    <section className="space-y-4">
      <h2 className="text-sm font-semibold">Default model</h2>
      <Field
        label="Default model"
        hint={saving ? 'Saving…' : 'Used when a route chain entry for this provider names no model.'}
      >
        <ModelInput
          value={defaultModel}
          onChange={setDefaultModel}
          onCommit={(v) => {
            setDefaultModel(v)
            void save(v)
          }}
          suggestions={suggestions}
          placeholder="model id"
          className="mt-1.5 h-10"
        />
      </Field>
    </section>
  )
}

// providerCatalogSearchLimit fetches this provider's whole candidate
// pool rather than the server's normal 50-row cap — a read-only
// browsing list should show everything, not just a first page, since
// there's no "load more" affordance here (only the search box narrows
// it further).
const providerCatalogSearchLimit = 200

// ProviderCatalogSection is a read-only, searchable view of every
// catalog model within this provider's candidate litellm_provider(s)
// — replaces the old manually-curated declared-models list entirely.
// Model *selection* for actual use lives solely on the Routes page now;
// this is purely "what's available and what it costs."
function ProviderCatalogSection({ provider }: { provider: AdminProvider }) {
  const [q, setQ] = useState('')
  const search = useCallback(
    (query: string) => catalogModelsForProvider(provider.id, query, providerCatalogSearchLimit),
    [provider.id],
  )
  const models = useCatalogSearch(q, search)

  return (
    <section className="space-y-4">
      <h2 className="text-sm font-semibold">Models · {models.length}</h2>
      <p className="text-sm text-muted-foreground">
        Every catalog model this provider can serve, with live pricing. Pick which one actually
        runs on the Routes page.
      </p>
      <Input
        value={q}
        onChange={(e) => setQ(e.target.value)}
        placeholder="filter by model id…"
        className="h-10"
      />
      <ul className="max-h-96 space-y-1.5 overflow-y-auto">
        {models.map((m) => {
          const id = catalogRowID(m)
          return (
            <li
              key={m.model_key}
              className="flex items-center gap-2 rounded-lg border border-border px-3 py-2 text-sm"
            >
              <span className="truncate font-mono">{id}</span>
              {m.max_input_tokens != null && (
                <span className="text-xs text-muted-foreground">
                  {Math.round(m.max_input_tokens / 1000)}k ctx
                </span>
              )}
              <span className="ml-auto shrink-0 font-mono text-xs text-muted-foreground">
                {priceLabel({ id, input_per_mtok: m.input_per_mtok, output_per_mtok: m.output_per_mtok })}
              </span>
            </li>
          )
        })}
        {models.length === 0 && (
          <li className="text-sm text-muted-foreground">no catalog models found</li>
        )}
      </ul>
    </section>
  )
}
