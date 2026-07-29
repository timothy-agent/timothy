import { ArrowLeft01Icon, Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import {
  availableModels,
  deleteProvider,
  deleteSecret,
  listProviders,
  patchProvider,
  secretStatus,
  setSecret,
  testProvider,
} from '../../api/client'
import type { AdminModel, AdminProvider, TestResult } from '../../api/types'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { Input } from '../ui/input'
import { ModelInput, type ModelSuggestion } from './ModelInput'
import { modelCatalog } from './modelCatalog'
import { matchPreset } from './presets'
import { ProviderLogo } from './ProviderLogo'
import { Field, Toggle } from './shared'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'
import { backendLabel, errText, stripPaste, secretField } from './util'

// ProviderEdit is a provider's own page for the controls too heavy for
// its summary card: rotating the stored key (or the Bedrock profile),
// and declaring which models it serves.
export function ProviderEdit() {
  const { id } = useParams()
  const navigate = useNavigate()
  const defaultBackend = useDefaultSecretBackend()

  const [provider, setProvider] = useState<AdminProvider | null | undefined>(undefined)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const refresh = useCallback(() => {
    listProviders()
      .then((list) => setProvider(list.find((p) => p.id === id) ?? null))
      .catch((err: unknown) => toast.error('Could not load provider', { description: errText(err) }))
  }, [id])
  useEffect(refresh, [refresh])

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
          <h1 className="truncate text-xl font-semibold tracking-tight">{provider.name}</h1>
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
          />
        )}
        {provider.driver === 'openaicompat' && (
          <ReasoningSection provider={provider} onChanged={refresh} />
        )}
        <ModelsSection provider={provider} onChanged={refresh} />
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
}: {
  provider: AdminProvider
  onChanged: () => void
  defaultBackend?: string
  bedrock?: boolean
}) {
  const [ref, setRef] = useState(provider.credential_ref)
  const [configured, setConfigured] = useState(false)
  const [storedBackend, setStoredBackend] = useState('')
  const [secretValue, setSecretValue] = useState('')
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

  const saveSecretValue = async () => {
    if (!ref || !secretValue) return
    setSavingSecret(true)
    try {
      await setSecret(ref, stripPaste(secretValue))
      setSecretValue('')
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
      setTest(await testProvider(provider.id))
    } catch (err) {
      setTest({ ok: false, latency_ms: 0, model: '', detail: errText(err) })
    } finally {
      setTesting(false)
    }
  }

  return (
    <section className="space-y-4">
      <h2 className="text-sm font-semibold">{bedrock ? 'AWS credentials' : 'API key'}</h2>

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
              ? `OK — ${test.model} answered in ${test.latency_ms} ms.`
              : test && !test.ok
                ? `Failed after ${test.latency_ms} ms: ${test.detail}`
                : 'Not tested yet.'}
        </span>
        <Button size="sm" variant="test" disabled={testing} onClick={() => void runTest()}>
          {testing ? 'Testing…' : 'Test connection'}
        </Button>
      </div>

      {bedrock ? (
        <div>
          <Field label="AWS profile">
            <Input
              value={ref}
              onChange={(e) => setRef(e.target.value)}
              onBlur={saveRef}
              placeholder="profile name"
              className="mt-1.5 h-10"
            />
          </Field>
          <p className="mt-1.5 text-sm text-muted-foreground">
            Bedrock signs with the AWS credential chain mounted into the gateway — no key is
            stored here, this only names the profile.
          </p>
        </div>
      ) : (
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
          <div className="flex gap-2">
            <Input
              type={secretField(defaultBackend ?? 'db', '').type}
              value={secretValue}
              onChange={(e) => setSecretValue(e.target.value)}
              placeholder={
                secretField(defaultBackend ?? 'db', configured ? 'paste new key to rotate' : 'paste key')
                  .placeholder
              }
              className="h-10"
              autoComplete="off"
            />
            <Button variant="outline" disabled={savingSecret || !secretValue || !ref} onClick={() => void saveSecretValue()}>
              Save
            </Button>
          </div>
          <p className="text-sm text-muted-foreground">
            {defaultBackend === 'vault'
              ? 'The key stays in Vault; only its path is saved. The default backend is set in the Secrets tab.'
              : defaultBackend === 'asm'
                ? 'The key stays in AWS Secrets Manager; only its name is saved. The default backend is set in the Secrets tab.'
                : defaultBackend === 'file'
                  ? 'The key stays in the mounted file; only its filename is saved. The default backend is set in the Secrets tab.'
                  : 'Encrypted with the master key and kept in Timothy’s database.'}
          </p>
          <details className="pt-1">
            <summary className="cursor-pointer text-sm font-medium text-muted-foreground transition hover:text-foreground">
              Advanced — reference name
            </summary>
            <Input
              value={ref}
              onChange={(e) => setRef(e.target.value)}
              onBlur={saveRef}
              placeholder="name (e.g. ANTHROPIC_API_KEY)"
              className="mt-1.5 h-10"
            />
            <p className="mt-1.5 text-sm text-muted-foreground">
              Storage name for this provider’s key — never the key itself. Change it only to
              share or repoint an already-stored secret.
            </p>
          </details>
        </div>
      )}
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
  const disabled = provider.options?.reasoning_effort === 'none'

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
    try {
      await patchProvider(provider.id, {
        options: trimmed ? { ...rest, request_timeout: trimmed } : rest,
      })
      onChanged()
    } catch (err) {
      setTimeoutValue(provider.options?.request_timeout ?? '')
      toast.error('Could not update request timeout', { description: errText(err) })
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
      <Field label="Request timeout" hint='Go duration, e.g. "20m" — empty uses the default'>
        <Input
          value={timeout}
          onChange={(e) => setTimeoutValue(e.target.value)}
          onBlur={() => void saveTimeout()}
          placeholder="5m"
          className="mt-1.5 h-10 max-w-40"
        />
      </Field>
    </section>
  )
}

// ModelsSection manages a provider's declared models: remove, set
// default, and add — via the provider's own model listing when the
// driver supports it (feeding the same autocomplete used on Add),
// manual entry otherwise.
function ModelsSection({ provider, onChanged }: { provider: AdminProvider; onChanged: () => void }) {
  const [fetched, setFetched] = useState<string[]>([])
  const [entry, setEntry] = useState('')
  const [embeddings, setEmbeddings] = useState(false)
  const [saving, setSaving] = useState(false)

  const declared = useMemo(() => new Set(provider.models.map((m) => m.id)), [provider.models])

  useEffect(() => {
    availableModels(provider.id)
      .then((models) => setFetched(models.map((m) => m.id)))
      .catch(() => setFetched([])) // 422 (bedrock) or fetch failure → manual entry only
  }, [provider.id])

  const preset = useMemo(() => matchPreset(provider), [provider])

  const suggestions: ModelSuggestion[] = useMemo(() => {
    const seen = new Map<string, ModelSuggestion>()
    for (const id of fetched) {
      if (!declared.has(id)) seen.set(id, { id })
    }
    for (const m of modelCatalog[preset.id] ?? []) {
      if (!declared.has(m.id) && !seen.has(m.id)) seen.set(m.id, { ...m, hint: 'catalog' })
    }
    return [...seen.values()]
  }, [fetched, declared, preset])

  const patchModels = async (models: AdminModel[], defaultModel?: string) => {
    setSaving(true)
    try {
      await patchProvider(provider.id, {
        models,
        ...(defaultModel !== undefined ? { default_model: defaultModel } : {}),
      })
      onChanged()
    } catch (err) {
      toast.error('Could not update models', { description: errText(err) })
    } finally {
      setSaving(false)
    }
  }

  const add = async () => {
    const trimmed = entry.trim()
    if (!trimmed || declared.has(trimmed)) return
    const prices = (modelCatalog[preset.id] ?? []).find((m) => m.id === trimmed)?.prices
    const model: AdminModel = {
      id: trimmed,
      ...(embeddings ? { capabilities: ['embeddings'] } : {}),
      ...(prices ? { prices } : {}),
    }
    const models = [...provider.models, model]
    await patchModels(models, embeddings ? undefined : provider.default_model || trimmed)
    setEntry('')
    setEmbeddings(false)
  }

  const remove = async (id: string) => {
    const models = provider.models.filter((m) => m.id !== id)
    const def = provider.default_model === id ? (models[0]?.id ?? '') : provider.default_model
    await patchModels(models, def)
  }

  return (
    <section className="space-y-4">
      <h2 className="text-sm font-semibold">Models · {provider.models.length}</h2>

      <ul className="space-y-1.5">
        {provider.models.map((m) => (
          <li
            key={m.id}
            className="flex items-center gap-2 rounded-lg border border-border px-3 py-2 text-sm"
          >
            <span className="truncate font-mono">{m.id}</span>
            {m.capabilities?.includes('embeddings') && (
              <span className="rounded bg-muted px-1.5 py-0.5 text-xs font-semibold text-muted-foreground">
                embeddings
              </span>
            )}
            {provider.default_model === m.id ? (
              <span className="rounded bg-brand-soft px-1.5 py-0.5 text-xs font-semibold text-brand-soft-foreground">
                default
              </span>
            ) : (
              <button
                type="button"
                disabled={saving}
                onClick={() => void patchModels(provider.models, m.id)}
                className="text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
              >
                set default
              </button>
            )}
            {m.context_window != null && (
              <span className="text-xs text-muted-foreground">
                {Math.round(m.context_window / 1000)}k ctx
              </span>
            )}
            <button
              type="button"
              aria-label={`Remove ${m.id}`}
              disabled={saving}
              onClick={() => void remove(m.id)}
              className="ml-auto text-muted-foreground hover:text-destructive"
            >
              <HugeiconsIcon icon={Delete02Icon} className="size-4" />
            </button>
          </li>
        ))}
        {provider.models.length === 0 && (
          <li className="text-sm text-muted-foreground">none declared</li>
        )}
      </ul>

      <div className="flex gap-2">
        <ModelInput
          value={entry}
          onChange={setEntry}
          suggestions={suggestions}
          placeholder="model id"
          className="h-10"
        />
        <Button variant="outline" disabled={!entry.trim() || saving} onClick={() => void add()}>
          Add
        </Button>
      </div>
      <label className="flex items-center gap-2 text-sm text-muted-foreground">
        <input
          type="checkbox"
          checked={embeddings}
          onChange={(e) => setEmbeddings(e.target.checked)}
          className="size-4 rounded border-border"
        />
        Embeddings model — routes the embedding route to this instead of chat
      </label>
    </section>
  )
}
