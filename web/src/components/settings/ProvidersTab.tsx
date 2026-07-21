import { Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import {
  availableModels,
  createProvider,
  deleteProvider,
  deleteSecret,
  listProviders,
  patchProvider,
  providersHealth,
  secretStatus,
  setSecret,
  testProvider,
  validateProvider,
} from '../../api/client'
import type {
  AdminModel,
  AdminProvider,
  ProviderHealth,
  TestResult,
} from '../../api/types'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { Input } from '../ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../ui/select'
import { matchPreset, providerPresets, type ProviderPreset } from './presets'
import { LogoSprite, ProviderLogo } from './ProviderLogo'
import { ErrorBanner, Field, Toggle } from './shared'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'
import { backendLabel, errText, secretField } from './util'

// stripPaste removes whitespace and zero-width characters that ride
// along when a key is copied out of wrapped text.
function stripPaste(v: string): string {
  return v.replace(/[\s\u200B-\u200D\u2060\uFEFF]/g, '')
}

// refFor derives a credential ref for a named provider instance: the
// preset's conventional env-style name, or one from the user's name.
function refFor(preset: ProviderPreset, name: string): string {
  if (preset.defaultRef) return preset.defaultRef
  const slug = name.trim().toUpperCase().replace(/[^A-Z0-9]+/g, '_')
  return slug ? `${slug}_API_KEY` : ''
}

export function ProvidersTab() {
  const [providers, setProviders] = useState<AdminProvider[]>([])
  const [health, setHealth] = useState<Record<string, ProviderHealth>>({})
  const [error, setError] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<AdminProvider | null>(null)
  const [connecting, setConnecting] = useState<ProviderPreset | null>(null)
  const defaultBackend = useDefaultSecretBackend()

  const refresh = useCallback(() => {
    Promise.all([listProviders(), providersHealth()])
      .then(([list, rows]) => {
        setProviders(list)
        setHealth(Object.fromEntries(rows.map((h) => [h.name, h])))
        setError(null)
      })
      .catch((err: unknown) => setError(errText(err)))
  }, [])
  useEffect(refresh, [refresh])

  const remove = async () => {
    if (!confirmDelete) return
    try {
      await deleteProvider(confirmDelete.id)
      setConfirmDelete(null)
      refresh()
    } catch (err) {
      setError(errText(err))
      setConfirmDelete(null)
    }
  }

  return (
    <div className="mt-6 space-y-4">
      <LogoSprite />
      <ErrorBanner message={error} />

      {providers.length > 0 && (
        <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Your providers · {providers.length}
        </h2>
      )}
      {providers.map((p) => (
        <ProviderCard
          key={p.id}
          provider={p}
          health={health[p.name]}
          defaultBackend={defaultBackend}
          onChanged={refresh}
          onDelete={() => setConfirmDelete(p)}
          onError={setError}
        />
      ))}
      {providers.length === 0 && !error && (
        <div className="rounded-xl border border-dashed p-10 text-center text-sm text-muted-foreground">
          No providers configured yet — connect one below.
        </div>
      )}

      <h2 className="pt-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Connect a provider
      </h2>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {providerPresets.map((preset) => (
          <button
            key={preset.id}
            type="button"
            onClick={() => setConnecting(preset)}
            className="flex items-center gap-3 rounded-xl border border-border p-3 text-left transition hover:border-zinc-400 hover:bg-zinc-50 dark:hover:border-zinc-600 dark:hover:bg-zinc-900"
          >
            <ProviderLogo preset={preset} className="size-8" />
            <span className="min-w-0">
              <span className="block text-sm font-medium">{preset.name}</span>
              <span className="block truncate text-xs text-muted-foreground">
                {preset.description}
              </span>
            </span>
            <span className="ml-auto text-muted-foreground" aria-hidden="true">
              +
            </span>
          </button>
        ))}
      </div>

      <ConnectDialog
        preset={connecting}
        defaultBackend={defaultBackend}
        onClose={() => setConnecting(null)}
        onAdded={() => {
          setConnecting(null)
          refresh()
        }}
      />

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

// ConnectDialog is the preset-aware add form: fields render from the
// preset (bedrock asks for region + profile, key providers for a key +
// storage), and "Validate & add" runs a real one-token completion
// before anything is persisted — a provider is born working or not at
// all.
function ConnectDialog({
  preset,
  defaultBackend,
  onClose,
  onAdded,
}: {
  preset: ProviderPreset | null
  defaultBackend: string
  onClose: () => void
  onAdded: () => void
}) {
  const [name, setName] = useState('')
  const [baseURL, setBaseURL] = useState('')
  const [region, setRegion] = useState('')
  const [profile, setProfile] = useState('')
  const [key, setKey] = useState('')
  const [ref, setRef] = useState('')
  const [refEdited, setRefEdited] = useState(false)
  const [model, setModel] = useState('')
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState<TestResult | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Reset per preset; the ref suggestion follows the name until edited.
  useEffect(() => {
    if (!preset) return
    setName(preset.id === 'custom' ? '' : preset.name)
    setBaseURL(preset.baseURL)
    setRegion(preset.region ?? '')
    setProfile('')
    setKey('')
    setRef(preset.id === 'custom' ? '' : refFor(preset, preset.name))
    setRefEdited(false)
    setModel(preset.validateModel)
    setBusy(false)
    setStatus(null)
    setError(null)
  }, [preset])

  if (!preset) return null
  const isBedrock = preset.driver === 'bedrock'
  const wantsKey = !isBedrock && preset.requiresKey
  const effectiveRef = isBedrock ? profile : ref
  const canSubmit =
    !busy && name.trim() !== '' && model.trim() !== '' && (isBedrock ? region.trim() !== '' : baseURL.trim() !== '' || preset.driver === 'anthropic')

  const submit = async () => {
    setBusy(true)
    setError(null)
    setStatus(null)
    const config = {
      name: name.trim(),
      kind: 'api',
      driver: preset.driver,
      // The bedrock driver keeps its region in base_url and the AWS
      // profile in credential_ref (see the registry contract).
      base_url: isBedrock ? region.trim() : baseURL.trim(),
      credential_ref: effectiveRef.trim(),
      headers: {},
    }
    try {
      if (wantsKey && key) {
        if (!ref.trim()) throw new Error('a credential reference name is required to store the key')
        await setSecret(ref.trim(), stripPaste(key))
      }
      const res = await validateProvider(config, model.trim())
      setStatus(res)
      if (!res.ok) return
      await createProvider({
        ...config,
        default_model: model.trim(),
        models: [{ id: model.trim() }],
        enabled: true,
      })
      onAdded()
    } catch (err) {
      setError(errText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open onOpenChange={(o) => !o && !busy && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            <span className="flex items-center gap-3">
              <ProviderLogo preset={preset} className="size-9" />
              <span>
                Connect {preset.name}
                <span className="block text-xs font-normal text-muted-foreground">
                  driver: {preset.driver}
                </span>
              </span>
            </span>
          </DialogTitle>
        </DialogHeader>

        <div className="space-y-3">
          <Field label="Name (unique)">
            <Input
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                if (!refEdited && !preset.defaultRef) setRef(refFor(preset, e.target.value))
              }}
              placeholder={preset.id === 'custom' ? 'my-gateway' : preset.name}
              className="mt-1 h-8"
            />
          </Field>

          {isBedrock ? (
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Region">
                <Input
                  value={region}
                  onChange={(e) => setRegion(e.target.value)}
                  placeholder="us-east-1"
                  className="mt-1 h-8"
                />
              </Field>
              <Field label="AWS profile (optional)">
                <Input
                  value={profile}
                  onChange={(e) => setProfile(e.target.value)}
                  placeholder="empty = credential chain"
                  className="mt-1 h-8"
                />
              </Field>
              <p className="text-xs text-muted-foreground sm:col-span-2">
                Bedrock signs with the AWS credential chain mounted into the gateway — no key is
                stored here.
              </p>
            </div>
          ) : (
            <>
              {preset.id === 'custom' && (
                <Field label="Base URL">
                  <Input
                    value={baseURL}
                    onChange={(e) => setBaseURL(e.target.value)}
                    placeholder="https://…/v1"
                    className="mt-1 h-8"
                  />
                </Field>
              )}
              {wantsKey &&
                (() => {
                  const field = secretField(defaultBackend, preset.keyPlaceholder ?? 'paste key')
                  return (
                    <div>
                      <Field label={preset.id === 'custom' ? 'API key (optional)' : 'API key'}>
                        <Input
                          type={field.type}
                          value={key}
                          onChange={(e) => setKey(e.target.value)}
                          placeholder={field.placeholder}
                          className="mt-1 h-8"
                          autoComplete="off"
                        />
                      </Field>
                      {(field.hint || preset.keyHint) && (
                        <p className="mt-1 text-xs text-muted-foreground">
                          {field.hint || preset.keyHint}
                        </p>
                      )}
                    </div>
                  )
                })()}
            </>
          )}

          <Field label="Model (validated with a one-token completion, becomes the default)">
            <Input
              value={model}
              onChange={(e) => setModel(e.target.value)}
              placeholder="model id"
              className="mt-1 h-8"
            />
          </Field>

          {!isBedrock && (
            <details>
              <summary className="cursor-pointer text-xs text-muted-foreground hover:text-foreground">
                Advanced — base URL &amp; credential reference
              </summary>
              <div className="mt-2 grid gap-3 sm:grid-cols-2">
                <Field label="Base URL">
                  <Input
                    value={baseURL}
                    onChange={(e) => setBaseURL(e.target.value)}
                    placeholder={preset.driver === 'anthropic' ? 'https://api.anthropic.com (default)' : 'https://…/v1'}
                    className="mt-1 h-8"
                  />
                </Field>
                <Field label="Credential reference">
                  <Input
                    value={ref}
                    onChange={(e) => {
                      setRef(e.target.value)
                      setRefEdited(true)
                    }}
                    placeholder="name (e.g. OPENAI_API_KEY)"
                    className="mt-1 h-8"
                  />
                </Field>
              </div>
            </details>
          )}

          <ErrorBanner message={error} />
          {status && !status.ok && (
            <div className="rounded-lg border border-red-500/30 bg-red-500/5 p-2.5 text-xs text-red-600 dark:text-red-400">
              Validation failed after {status.latency_ms} ms: {status.detail}
            </div>
          )}
        </div>

        <DialogFooter>
          <span className="mr-auto text-xs text-muted-foreground">
            {busy ? `Sending test completion to ${model.trim()}…` : ''}
          </span>
          <Button variant="outline" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={!canSubmit} onClick={() => void submit()}>
            {busy ? 'Validating…' : 'Validate & add'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ProviderCard({
  provider,
  health,
  defaultBackend,
  onChanged,
  onDelete,
  onError,
}: {
  provider: AdminProvider
  health?: ProviderHealth
  defaultBackend: string
  onChanged: () => void
  onDelete: () => void
  onError: (msg: string) => void
}) {
  const preset = matchPreset(provider)
  const [ref, setRef] = useState(provider.credential_ref)
  const [testing, setTesting] = useState(false)
  const [test, setTest] = useState<TestResult | null>(null)
  const [configured, setConfigured] = useState(false)
  const [storedBackend, setStoredBackend] = useState('')
  const [secretValue, setSecretValue] = useState('')
  const [savingSecret, setSavingSecret] = useState(false)

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

  const toggle = (enabled: boolean) => {
    patchProvider(provider.id, { enabled }).then(onChanged, (err: unknown) =>
      onError(errText(err)),
    )
  }

  const saveRef = () => {
    if (ref === provider.credential_ref) return
    patchProvider(provider.id, { credential_ref: ref }).then(onChanged, (err: unknown) => {
      // Revert on rejection: a mistakenly pasted secret must not linger
      // in this field's state after the server refuses it.
      setRef(provider.credential_ref)
      onError(errText(err))
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
    } catch (err) {
      onError(errText(err))
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
      onError(errText(err))
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
    <div className="rounded-xl border border-border p-4">
      <div className="flex flex-wrap items-center gap-3">
        <ProviderLogo preset={preset} />
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium">{provider.name}</span>
            <span className="text-xs text-muted-foreground">{provider.driver}</span>
          </div>
          <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <span
              className={`size-2 rounded-full ${health?.healthy ? 'bg-emerald-500' : 'bg-red-500'}`}
            />
            {health?.healthy ? 'healthy' : 'credential missing'}
          </div>
        </div>
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

      <div className="mt-4 grid gap-4 sm:grid-cols-2">
        {provider.driver === 'bedrock' ? (
          <div>
            <span className="text-xs font-medium">AWS profile</span>
            <Input
              value={ref}
              onChange={(e) => setRef(e.target.value)}
              onBlur={saveRef}
              placeholder="profile name"
              className="mt-1.5 h-8"
            />
            <p className="mt-1.5 text-xs text-muted-foreground">
              Bedrock signs with the AWS credential chain mounted into the gateway — no key is
              stored here, this only names the profile.
            </p>
          </div>
        ) : (
          <div>
            <div className="flex items-center gap-2">
              <span className="text-xs font-medium">API key</span>
              <span
                className={`rounded px-1.5 py-px text-[10px] font-medium uppercase ${
                  configured
                    ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400'
                    : 'bg-amber-500/15 text-amber-600 dark:text-amber-400'
                }`}
              >
                {configured ? `stored · ${backendLabel(storedBackend)}` : 'not set'}
              </span>
              {configured && (
                <button
                  type="button"
                  disabled={savingSecret}
                  onClick={() => void clearSecretValue()}
                  className="text-xs text-muted-foreground underline-offset-2 hover:text-red-500 hover:underline"
                >
                  clear
                </button>
              )}
            </div>
            <div className="mt-1.5 flex gap-2">
              <Input
                type={secretField(defaultBackend, '').type}
                value={secretValue}
                onChange={(e) => setSecretValue(e.target.value)}
                placeholder={
                  secretField(defaultBackend, configured ? 'paste new key to rotate' : 'paste key')
                    .placeholder
                }
                className="h-8"
                autoComplete="off"
              />
              <Button
                size="sm"
                variant="outline"
                disabled={savingSecret || !secretValue || !ref}
                onClick={() => void saveSecretValue()}
              >
                Save
              </Button>
            </div>
            <p className="mt-1.5 text-xs text-muted-foreground">
              {defaultBackend === 'vault'
                ? 'The key stays in Vault; only its path is saved. The default backend is set in the Secrets tab.'
                : defaultBackend === 'asm'
                  ? 'The key stays in AWS Secrets Manager; only its name is saved. The default backend is set in the Secrets tab.'
                  : 'Encrypted with the master key and kept in Timothy’s database.'}
            </p>
            <details className="mt-2">
              <summary className="cursor-pointer text-xs text-muted-foreground hover:text-foreground">
                Advanced — reference name
              </summary>
              <Input
                value={ref}
                onChange={(e) => setRef(e.target.value)}
                onBlur={saveRef}
                placeholder="name (e.g. ANTHROPIC_API_KEY)"
                className="mt-1.5 h-8"
              />
              <p className="mt-1 text-xs text-muted-foreground">
                Storage name for this provider’s key — never the key itself. Change it only to
                share or repoint an already-stored secret.
              </p>
            </details>
          </div>
        )}
        <ModelsEditor provider={provider} onChanged={onChanged} onError={onError} />
      </div>
    </div>
  )
}

// ModelsEditor manages a provider's declared models: remove, set
// default, and add — via the provider's own model listing when the
// driver supports it, manual entry otherwise (the fetch ladder).
function ModelsEditor({
  provider,
  onChanged,
  onError,
}: {
  provider: AdminProvider
  onChanged: () => void
  onError: (msg: string) => void
}) {
  const [adding, setAdding] = useState(false)
  const [fetching, setFetching] = useState(false)
  const [choices, setChoices] = useState<string[] | null>(null)
  const [manual, setManual] = useState(false)
  const [entry, setEntry] = useState('')
  const [saving, setSaving] = useState(false)

  const declared = new Set(provider.models.map((m) => m.id))

  const startAdd = () => {
    setAdding(true)
    setEntry('')
    setManual(false)
    setChoices(null)
    setFetching(true)
    availableModels(provider.id)
      .then((models) => {
        const fresh = models.map((m) => m.id).filter((id) => !declared.has(id))
        if (fresh.length === 0) setManual(true)
        else setChoices(fresh)
      })
      .catch(() => setManual(true)) // 422 (bedrock) or fetch failure → manual entry
      .finally(() => setFetching(false))
  }

  const patchModels = async (models: AdminModel[], defaultModel?: string) => {
    setSaving(true)
    try {
      await patchProvider(provider.id, {
        models,
        ...(defaultModel !== undefined ? { default_model: defaultModel } : {}),
      })
      onChanged()
    } catch (err) {
      onError(errText(err))
    } finally {
      setSaving(false)
    }
  }

  const add = async (id: string) => {
    const trimmed = id.trim()
    if (!trimmed || declared.has(trimmed)) return
    const models = [...provider.models, { id: trimmed }]
    await patchModels(models, provider.default_model || trimmed)
    setAdding(false)
  }

  const remove = async (id: string) => {
    const models = provider.models.filter((m) => m.id !== id)
    const def =
      provider.default_model === id ? (models[0]?.id ?? '') : provider.default_model
    await patchModels(models, def)
  }

  return (
    <div className="text-xs text-muted-foreground">
      <div className="flex items-center gap-2">
        Models · {provider.models.length}
        <button
          type="button"
          disabled={saving}
          onClick={adding ? () => setAdding(false) : startAdd}
          className="ml-auto text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
        >
          {adding ? 'close' : '+ add model'}
        </button>
      </div>
      <ul className="mt-1 space-y-1">
        {provider.models.map((m) => (
          <li key={m.id} className="flex items-center gap-2 text-sm text-foreground">
            <span className="truncate">{m.id}</span>
            {provider.default_model === m.id ? (
              <span className="rounded bg-blue-500/10 px-1.5 py-px text-[10px] text-blue-600 dark:text-blue-400">
                default
              </span>
            ) : (
              <button
                type="button"
                disabled={saving}
                onClick={() => void patchModels(provider.models, m.id)}
                className="rounded px-1.5 py-px text-[10px] text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
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
              className="ml-auto text-muted-foreground hover:text-red-500"
            >
              ✕
            </button>
          </li>
        ))}
        {provider.models.length === 0 && <li className="text-xs">none declared</li>}
      </ul>

      {adding && (
        <div className="mt-2">
          {fetching && <p className="text-xs italic">Loading available models…</p>}
          {!fetching && choices && !manual && (
            <div className="flex gap-2">
              <Select value={entry} onValueChange={setEntry}>
                <SelectTrigger className="h-8 w-full text-xs" aria-label="available models">
                  <SelectValue placeholder="pick a model…" />
                </SelectTrigger>
                <SelectContent>
                  {choices.map((id) => (
                    <SelectItem key={id} value={id}>
                      {id}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Button
                size="sm"
                variant="outline"
                disabled={!entry || saving}
                onClick={() => void add(entry)}
              >
                Add
              </Button>
            </div>
          )}
          {!fetching && manual && (
            <div>
              <div className="flex gap-2">
                <Input
                  aria-label="model id"
                  value={entry}
                  onChange={(e) => setEntry(e.target.value)}
                  placeholder="model id"
                  className="h-8"
                />
                <Button
                  size="sm"
                  variant="outline"
                  disabled={!entry.trim() || saving}
                  onClick={() => void add(entry)}
                >
                  Add
                </Button>
              </div>
              <p className="mt-1 text-xs">
                This provider doesn’t list its models — enter the id manually.
              </p>
            </div>
          )}
          {!fetching && !manual && choices && (
            <button
              type="button"
              onClick={() => setManual(true)}
              className="mt-1 text-xs underline-offset-2 hover:text-foreground hover:underline"
            >
              enter manually instead
            </button>
          )}
        </div>
      )}
    </div>
  )
}
