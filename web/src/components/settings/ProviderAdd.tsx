import { AlertCircleIcon, ArrowLeft01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useMemo, useState } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { createProvider, listProviders, setSecret, validateProvider } from '../../api/client'
import type { AdminProvider, TestResult } from '../../api/types'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { ModelInput, type ModelSuggestion } from './ModelInput'
import { modelCatalog } from './modelCatalog'
import { bedrockRegions, providerPresets, type ProviderPreset } from './presets'
import { ProviderLogo } from './ProviderLogo'
import { Field } from './shared'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'
import { errText, secretDestination, stripPaste } from './util'

// refFor derives a credential ref for a named provider instance: the
// preset's conventional storage-key name, or one from the user's name.
function refFor(preset: ProviderPreset, name: string): string {
  if (preset.defaultRef) return preset.defaultRef
  const slug = name.trim().toUpperCase().replace(/[^A-Z0-9]+/g, '_')
  return slug ? `${slug}_API_KEY` : ''
}

// ProviderAdd is its own page (not a dialog): connecting a provider is
// a create action, and validation runs a real one-token completion
// against the unsaved config — a provider is born working or not at
// all. The Add button stays disabled until that test has passed;
// editing any field after a passing test re-locks it, since the
// config it validated no longer matches what's on screen.
export function ProviderAdd() {
  const { presetId } = useParams()
  const navigate = useNavigate()
  const preset = providerPresets.find((p) => p.id === presetId)
  const defaultBackend = useDefaultSecretBackend()

  const [name, setName] = useState('')
  const [baseURL, setBaseURL] = useState('')
  const [region, setRegion] = useState('us-east-1')
  const [key, setKey] = useState('')
  const [accessKeyId, setAccessKeyId] = useState('')
  const [secretAccessKey, setSecretAccessKey] = useState('')
  const [ref, setRef] = useState('')
  const [refEdited, setRefEdited] = useState(false)
  const [model, setModel] = useState('')
  const [busy, setBusy] = useState(false)
  const [keyError, setKeyError] = useState<string | null>(null)
  const [test, setTest] = useState<TestResult | null>(null)
  const [tested, setTested] = useState(false)
  const [existing, setExisting] = useState<AdminProvider[]>([])

  useEffect(() => {
    listProviders().then(setExisting, () => undefined)
  }, [])

  useEffect(() => {
    if (!preset) return
    setName(preset.id === 'custom' ? '' : preset.name)
    setBaseURL(preset.baseURL)
    setRegion(preset.region ?? 'us-east-1')
    setKey('')
    setAccessKeyId('')
    setSecretAccessKey('')
    setRef(preset.id === 'custom' ? '' : refFor(preset, preset.name))
    setRefEdited(false)
    setModel(preset.validateModel)
    setBusy(false)
    setKeyError(null)
    setTest(null)
    setTested(false)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [preset?.id])

  // Model suggestions: real ids declared on other providers that share
  // BOTH this driver and this base_url — same driver alone isn't
  // enough (openaicompat covers OpenAI, GLM, Ollama, Grok, and more,
  // none of whose models work against each other's endpoint). Bedrock
  // has no base_url to key on (region lives in options.region instead),
  // and its inference-profile model ids aren't region-specific, so any
  // other bedrock provider's declared models qualify. Plus the preset's
  // own validated default, and the static catalog for this preset.
  // Advisory only, never blocks a free-typed id.
  const modelSuggestions: ModelSuggestion[] = useMemo(() => {
    if (!preset) return []
    const seen = new Map<string, ModelSuggestion>()
    const catalog = modelCatalog[preset.id] ?? []
    const nameFor = (id: string) => catalog.find((m) => m.id === id)?.name
    for (const p of existing) {
      if (p.driver !== preset.driver) continue
      if (preset.driver !== 'bedrock' && p.base_url.trim() !== baseURL.trim()) continue
      for (const m of p.models) {
        if (!seen.has(m.id)) seen.set(m.id, { id: m.id, name: nameFor(m.id), hint: `on ${p.name}` })
      }
    }
    if (preset.validateModel && !seen.has(preset.validateModel)) {
      seen.set(preset.validateModel, {
        id: preset.validateModel,
        name: nameFor(preset.validateModel),
        hint: 'preset default',
      })
    }
    for (const m of catalog) {
      if (!seen.has(m.id)) seen.set(m.id, { ...m, hint: 'catalog' })
    }
    return [...seen.values()]
  }, [existing, preset, baseURL])

  if (!preset) return <Navigate to="/settings/providers" replace />

  const isBedrock = preset.driver === 'bedrock'
  const wantsKey = preset.requiresKey

  // Bedrock always splits into access key id / secret access key —
  // every backend now writes through the raw value Timothy is given,
  // so the split no longer depends on which backend is default.
  const bedrockSplit = isBedrock
  const bedrockKeyJSON = () =>
    JSON.stringify({
      access_key_id: stripPaste(accessKeyId.trim()),
      secret_access_key: stripPaste(secretAccessKey.trim()),
    })
  const hasKey = bedrockSplit ? !!(accessKeyId.trim() && secretAccessKey.trim()) : !!key.trim()

  // Any edit invalidates a previous test — the config it validated no
  // longer matches what's on screen.
  const invalidate = () => {
    if (tested) {
      setTested(false)
      setTest(null)
    }
    setKeyError(null)
  }

  const runTest = async () => {
    if (wantsKey && !hasKey) {
      setKeyError(
        bedrockSplit
          ? 'An access key ID and secret access key are required to test this provider.'
          : 'An API key is required to test this provider.',
      )
      return
    }
    if (!name.trim()) {
      setKeyError(null)
      toast.error('Name required', { description: 'Give this provider a unique name before testing.' })
      return
    }
    setBusy(true)
    setTest(null)
    try {
      if (wantsKey && hasKey) {
        if (!ref.trim()) throw new Error('a credential reference name is required to store the key')
        await setSecret(ref.trim(), bedrockSplit ? bedrockKeyJSON() : stripPaste(key))
      }
      const config = {
        name: name.trim(),
        kind: 'api',
        driver: preset.driver,
        base_url: baseURL.trim(),
        credential_ref: ref.trim(),
        headers: {},
        ...(isBedrock ? { options: { region } } : {}),
      }
      const res = await validateProvider(config, model.trim())
      setTest(res)
      setTested(res.ok)
    } catch (err) {
      setTest({ ok: false, latency_ms: 0, model: model.trim(), detail: errText(err) })
      setTested(false)
    } finally {
      setBusy(false)
    }
  }

  const submit = async () => {
    if (!tested) return
    setBusy(true)
    try {
      const trimmedModel = model.trim()
      const prices = (modelCatalog[preset.id] ?? []).find((m) => m.id === trimmedModel)?.prices
      await createProvider({
        name: name.trim(),
        kind: 'api',
        driver: preset.driver,
        base_url: baseURL.trim(),
        credential_ref: ref.trim(),
        headers: {},
        default_model: trimmedModel,
        models: [{ id: trimmedModel, ...(prices ? { prices } : {}) }],
        enabled: true,
        ...(isBedrock ? { options: { region } } : {}),
      })
      toast.success('Provider added', { description: `${name.trim()} is connected and ready to route to.` })
      navigate('/settings/providers')
    } catch (err) {
      toast.error('Could not add provider', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

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
        <ProviderLogo preset={preset} className="size-12" />
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Add {preset.name}</h1>
          <p className="text-sm text-muted-foreground">driver: {preset.driver}</p>
        </div>
      </div>

      <div className="grid max-w-3xl gap-5">
        <Field label="Name (unique)">
          <Input
            value={name}
            onChange={(e) => {
              setName(e.target.value)
              if (!refEdited && !preset.defaultRef) setRef(refFor(preset, e.target.value))
              invalidate()
            }}
            placeholder={preset.id === 'custom' ? 'my-gateway' : preset.name}
            className="mt-1.5 h-10"
          />
        </Field>

        {isBedrock && (
          <Field label="Region">
            <Select
              value={region}
              onValueChange={(v) => {
                setRegion(v)
                invalidate()
              }}
            >
              <SelectTrigger className="mt-1.5 h-10 w-full">
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
        )}

        {preset.id === 'custom' && (
          <Field label="Base URL">
            <Input
              value={baseURL}
              onChange={(e) => {
                setBaseURL(e.target.value)
                invalidate()
              }}
              placeholder="https://…/v1"
              className="mt-1.5 h-10"
            />
          </Field>
        )}
        {wantsKey && bedrockSplit && (
          <div>
            <div className="grid gap-5 sm:grid-cols-2">
              <Field label="Access Key ID">
                <Input
                  type="password"
                  value={accessKeyId}
                  onChange={(e) => {
                    setAccessKeyId(e.target.value)
                    invalidate()
                  }}
                  placeholder="AKIA…"
                  className="mt-1.5 h-10"
                  autoComplete="off"
                  aria-invalid={keyError != null}
                />
              </Field>
              <Field label="Secret Access Key">
                <Input
                  type="password"
                  value={secretAccessKey}
                  onChange={(e) => {
                    setSecretAccessKey(e.target.value)
                    invalidate()
                  }}
                  placeholder="wJalrXUtnFEMI/K7MDEN..."
                  className="mt-1.5 h-10"
                  autoComplete="off"
                  aria-invalid={keyError != null}
                />
              </Field>
            </div>
            {keyError && (
              <p className="mt-1.5 flex items-center gap-1.5 text-sm font-medium text-destructive">
                <HugeiconsIcon icon={AlertCircleIcon} className="size-4 shrink-0" />
                {keyError}
              </p>
            )}
          </div>
        )}
        {wantsKey && !bedrockSplit && (
          <div>
            <Field label={preset.id === 'custom' ? 'API key (optional)' : 'API key'}>
              <Input
                type="password"
                value={key}
                onChange={(e) => {
                  setKey(e.target.value)
                  invalidate()
                }}
                placeholder={preset.keyPlaceholder ?? 'paste key'}
                className="mt-1.5 h-10"
                autoComplete="off"
                aria-invalid={keyError != null}
              />
            </Field>
            {keyError && (
              <p className="mt-1.5 flex items-center gap-1.5 text-sm font-medium text-destructive">
                <HugeiconsIcon icon={AlertCircleIcon} className="size-4 shrink-0" />
                {keyError}
              </p>
            )}
            {!keyError && (
              <div className="mt-1.5 space-y-1 text-sm text-muted-foreground">
                {preset.keyHint && (
                  <p>
                    {preset.keyHint}
                    {preset.keyURL && (
                      <>
                        {' '}
                        <a
                          href={preset.keyURL}
                          target="_blank"
                          rel="noreferrer"
                          className="font-medium text-primary underline underline-offset-2 hover:no-underline"
                        >
                          Open {preset.name} →
                        </a>
                      </>
                    )}
                  </p>
                )}
                <p>{secretDestination(defaultBackend, ref)}</p>
              </div>
            )}
          </div>
        )}

        <Field label="Model" hint="validated with a one-token completion, becomes the default">
          <ModelInput
            value={model}
            onChange={(v) => {
              setModel(v)
              invalidate()
            }}
            suggestions={modelSuggestions}
            placeholder="model id"
            className="mt-1.5 h-10"
          />
        </Field>

        <details className="group">
          <summary className="cursor-pointer text-sm font-medium text-muted-foreground transition hover:text-foreground">
            Advanced: {isBedrock ? 'credential reference' : 'base URL & credential reference'}
          </summary>
          <div className="mt-3 grid gap-5 sm:grid-cols-2">
            {!isBedrock && (
              <Field label="Base URL">
                <Input
                  value={baseURL}
                  onChange={(e) => {
                    setBaseURL(e.target.value)
                    invalidate()
                  }}
                  placeholder={preset.driver === 'anthropic' ? 'https://api.anthropic.com (default)' : 'https://…/v1'}
                  className="mt-1.5 h-10"
                />
              </Field>
            )}
            <Field label="Credential reference">
              <Input
                value={ref}
                onChange={(e) => {
                  setRef(e.target.value)
                  setRefEdited(true)
                  invalidate()
                }}
                placeholder="name (e.g. OPENAI_API_KEY)"
                className="mt-1.5 h-10"
              />
            </Field>
          </div>
        </details>

        <div
          className={
            'flex flex-wrap items-center gap-3 rounded-xl border p-4 text-sm ' +
            (tested
              ? 'border-good/30 bg-good-soft text-good'
              : test && !test.ok
                ? 'border-destructive/30 bg-destructive/5 text-destructive'
                : 'border-border bg-muted/40 text-muted-foreground')
          }
        >
          <span className="min-w-0 flex-1 font-medium">
            {busy
              ? `Sending test completion to ${model.trim() || '…'}…`
              : tested
                ? `OK, ${test?.model} answered in ${test?.latency_ms} ms.`
                : test && !test.ok
                  ? `Failed after ${test.latency_ms} ms: ${test.detail}`
                  : 'Not tested yet, run a test before adding.'}
          </span>
          <Button size="sm" variant="test" disabled={busy} onClick={() => void runTest()}>
            {busy ? 'Testing…' : 'Test connection'}
          </Button>
        </div>

        <div className="flex gap-3 pt-2">
          <Button variant="outline" disabled={busy} onClick={() => navigate('/settings/providers')}>
            Cancel
          </Button>
          <Button disabled={!tested || busy} onClick={() => void submit()}>
            Add provider
          </Button>
        </div>
      </div>
    </div>
  )
}
