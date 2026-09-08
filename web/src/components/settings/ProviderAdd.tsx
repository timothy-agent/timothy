import { CircleAlert } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { createProvider, searchCatalog, setSecret, validateProvider } from '../../api/client'
import type { TestResult } from '../../api/types'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { Field, FieldGroup, Form, FormActions } from '../timothy/field'
import { bedrockKeyJSON, BedrockKeyFields } from './BedrockKeyFields'
import { CredentialField, type CredentialMode } from './CredentialRefPicker'
import { catalogMatchForID, catalogRowID, ModelPicker, type ModelSuggestion, useCatalogSearch } from './ModelPicker'
import { bedrockRegions, providerPresets, type ProviderPreset } from '../../lib/providerPresets'
import { ProviderLogo } from '../timothy/provider-logo'
import { settingsArea } from './settingsAreas'
import { TestStatus } from './TestStatus'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'
import { probeFailureText, responsesSuffix, secretDestination, stripPaste } from './util'
import { errText, isTimothyAuthDetail, isTimothyAuthError } from '../../lib/errors'

const area = settingsArea('providers')

// refFor derives a credential ref for a named provider instance: the
// preset's conventional storage-key name, or one from the user's name.
function refFor(preset: ProviderPreset, name: string): string {
  if (preset.defaultRef) return preset.defaultRef
  const slug = name.trim().toUpperCase().replace(/[^A-Z0-9]+/g, '_')
  return slug ? `${slug}_API_KEY` : ''
}

// presetLitellmProvider maps a preset id to the litellm_provider value
// its models are filed under in the synced catalog, mirroring
// catalog.CandidateProviders' driver/host mapping for this fixed set
// of presets. '' means no restriction, same as an unrecognized
// driver/host server-side.
function presetLitellmProvider(presetId: string): string {
  return {
    openai: 'openai',
    anthropic: 'anthropic',
    bedrock: 'bedrock',
    glm: 'zai',
    grok: 'xai',
    ollama: 'ollama',
  }[presetId] ?? ''
}

// AnthropicAuthMode is the Anthropic preset's auth picker (D-051,
// folded into the single Anthropic preset): the plain metered API key
// (default, today's kind=api flow, unchanged) or a Claude subscription
// OAuth token, which creates a kind='cli' row instead.
type AnthropicAuthMode = 'api_key' | 'oauth'

// ProviderAdd is its own page (not a dialog): connecting a provider is
// a create action, and validation runs a real one-token completion
// against the unsaved config, a provider is born working or not at
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
  const [cliModel, setCliModel] = useState('claude-sonnet-4-6')
  const [busy, setBusy] = useState(false)
  const [keyError, setKeyError] = useState<string | null>(null)
  const [test, setTest] = useState<TestResult | null>(null)
  const [tested, setTested] = useState(false)
  const [anthropicAuth, setAnthropicAuth] = useState<AnthropicAuthMode>('api_key')
  // credMode picks between typing a new secret (default) and reusing a
  // stored ref, only offered for the plain (non-bedrock-split,
  // non-CLI) API key flow, the common reuse case (e.g. the same
  // OpenAI-compatible key across two provider rows).
  const [credMode, setCredMode] = useState<CredentialMode>('new')

  // Live type-ahead over this preset's catalog rows, keyed on the
  // typed model id, presetLitellmProvider mirrors the gateway's
  // catalog.CandidateProviders mapping for the fixed set of presets
  // this form offers; an unmapped preset searches the whole catalog,
  // same as an unrecognized driver/host does server-side.
  const catalogSearch = useCallback(
    (q: string) => (preset ? searchCatalog(q, presetLitellmProvider(preset.id)) : Promise.resolve([])),
    [preset],
  )
  const catalogModels = useCatalogSearch(model, catalogSearch)

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
    setCliModel(preset.id === 'cursor' ? 'composer-2.5' : 'claude-sonnet-4-6')
    setBusy(false)
    setKeyError(null)
    setTest(null)
    setTested(false)
    setAnthropicAuth('api_key')
    setCredMode('new')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [preset?.id])

  // Model suggestions: the preset's own validated default, plus the
  // live synced catalog for this preset (ids only, the catalog
  // carries no friendly names). Advisory only, never blocks a
  // free-typed id.
  const modelSuggestions: ModelSuggestion[] = useMemo(() => {
    if (!preset) return []
    const seen = new Map<string, ModelSuggestion>()
    if (preset.validateModel && !seen.has(preset.validateModel)) {
      const catalogMatch = catalogMatchForID(preset.validateModel, catalogModels)
      seen.set(preset.validateModel, {
        id: preset.validateModel,
        input_per_mtok: catalogMatch?.input_per_mtok,
        output_per_mtok: catalogMatch?.output_per_mtok,
      })
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
  }, [preset, catalogModels])

  if (!preset) return <Navigate to="/settings/providers" replace />

  const isBedrock = preset.driver === 'bedrock'
  const isAnthropic = preset.id === 'anthropic'
  const isCursor = preset.id === 'cursor'
  // isCli: the subscription-token auth mode (Anthropic) or a CLI-only
  // preset (Cursor) creates a kind='cli' row (D-051) instead of the
  // plain kind='api' key flow.
  const isCli = (isAnthropic && anthropicAuth === 'oauth') || isCursor
  const wantsKey = preset.requiresKey

  // Bedrock always splits into access key id / secret access key,
  // every backend now writes through the raw value Timothy is given,
  // so the split no longer depends on which backend is default.
  const bedrockSplit = isBedrock
  const usingExistingCred = !isCli && !bedrockSplit && credMode === 'existing'
  const hasKey = usingExistingCred
    ? !!ref.trim()
    : bedrockSplit
      ? !!(accessKeyId.trim() && secretAccessKey.trim())
      : !!key.trim()

  // Any edit invalidates a previous test, the config it validated no
  // longer matches what's on screen.
  const invalidate = () => {
    if (tested) {
      setTested(false)
      setTest(null)
    }
    setKeyError(null)
  }

  // submitCli validates the pasted subscription token, stores it, and
  // creates the kind='cli' row. No probe: no chat driver exists for
  // these rows (D-051), so there is nothing to test against. Anthropic
  // subscription tokens must carry the sk-ant-oat prefix; Cursor's API
  // key has no fixed prefix to check, so any non-empty value passes.
  const submitCli = async () => {
    if (!name.trim()) {
      toast.error('Name required', { description: 'Give this provider a unique name before adding.' })
      return
    }
    const trimmedKey = stripPaste(key)
    if (!trimmedKey) {
      setKeyError(isCursor ? 'An API key is required.' : 'A subscription token is required.')
      return
    }
    if (!isCursor && !trimmedKey.startsWith('sk-ant-oat')) {
      setKeyError('Subscription tokens start with sk-ant-oat. Run `claude setup-token` to generate one.')
      return
    }
    if (!ref.trim()) {
      setKeyError('a credential reference name is required to store it')
      return
    }
    setKeyError(null)
    setBusy(true)
    try {
      await setSecret(ref.trim(), trimmedKey)
      await createProvider({
        name: name.trim(),
        kind: 'cli',
        // Anthropic's preset.driver is 'anthropic' (its api-kind
        // flow); the oauth mode's kind='cli' row is always claude-cli.
        // Cursor is a CLI-only preset, so its driver is used as-is.
        driver: isCursor ? preset.driver : 'claude-cli',
        base_url: '',
        credential_ref: ref.trim(),
        headers: {},
        default_model: cliModel.trim(),
        enabled: true,
      })
      toast.success('Provider added', { description: `${name.trim()} is ready for coding missions.` })
      navigate('/settings/providers')
    } catch (err) {
      toast.error('Could not add provider', { description: errText(err) })
    } finally {
      setBusy(false)
    }
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
    if (!usingExistingCred && isAnthropic && anthropicAuth === 'api_key' && stripPaste(key).startsWith('sk-ant-oat')) {
      setKeyError('This looks like a subscription token, use "Subscription token" instead.')
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
      if (wantsKey && hasKey && !usingExistingCred) {
        if (!ref.trim()) throw new Error('a credential reference name is required to store the key')
        await setSecret(ref.trim(), bedrockSplit ? bedrockKeyJSON(accessKeyId, secretAccessKey) : stripPaste(key))
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
      if (!res.ok && isTimothyAuthDetail(res.detail)) {
        setTest(null)
        setTested(false)
        return
      }
      setTest(res)
      setTested(res.ok)
    } catch (err) {
      setTested(false)
      // Timothy's own bearer failed, App opens the token dialog.
      // Do not paint this as a provider probe miss (0 ms + the raw
      // "missing or invalid bearer token" string).
      if (isTimothyAuthError(err)) {
        setTest(null)
        return
      }
      setTest({ ok: false, latency_ms: 0, model: model.trim(), detail: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  const submit = async () => {
    if (!tested) return
    setBusy(true)
    try {
      const trimmedModel = model.trim()
      // Seeds options.litellm_provider from the same preset->provider
      // map this form's own catalog search already uses, an explicit
      // record of which catalog section this provider's models are
      // priced under, so ProviderEdit's field starts pre-filled rather
      // than needing a manual first save. Unset (custom preset) leaves
      // the driver/host heuristic to keep working as it always has.
      const litellmProvider = presetLitellmProvider(preset.id)
      const options = {
        ...(isBedrock ? { region } : {}),
        ...(litellmProvider ? { litellm_provider: litellmProvider } : {}),
      }
      await createProvider({
        name: name.trim(),
        kind: 'api',
        driver: preset.driver,
        base_url: baseURL.trim(),
        credential_ref: ref.trim(),
        headers: {},
        default_model: trimmedModel,
        enabled: true,
        ...(Object.keys(options).length > 0 ? { options } : {}),
      })
      toast.success('Provider added', { description: `${name.trim()} is connected and ready to route to.` })
      navigate('/settings/providers')
    } catch (err) {
      toast.error('Could not add provider', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  // The probe gate caption is always visible (contract 10.5: the
  // reason a gated primary is disabled must be visible next to it),
  // so this never collapses to TestStatus's idle (nothing rendered).
  // "not tested yet" is its own gate state, not silence.
  const testState: 'gate' | 'testing' | 'ok' | 'failed' = busy
    ? 'testing'
    : tested
      ? 'ok'
      : test && !test.ok
        ? 'failed'
        : 'gate'

  return (
    <PageShell width="form">
      <PageHeader
        title={`Add ${preset.name}`}
        description={`driver: ${preset.driver}`}
        meta={<ProviderLogo preset={preset} className="size-9" />}
        breadcrumbs={[
          { label: 'Settings', href: '/settings' },
          { label: area.label, href: '/settings/providers' },
          { label: `Add ${preset.name}` },
        ]}
      />

      <Form onSubmit={(e) => e.preventDefault()}>
        <FieldGroup>
          <Field label="Name (unique)">
            <Input
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                if (!refEdited && !preset.defaultRef) setRef(refFor(preset, e.target.value))
                invalidate()
              }}
              placeholder={preset.id === 'custom' ? 'my-gateway' : preset.name}
            />
          </Field>

          {isAnthropic && (
            <Field label="Auth">
              {(props) => (
                <>
                  <Select
                    value={anthropicAuth}
                    onValueChange={(v) => {
                      setAnthropicAuth(v as AnthropicAuthMode)
                      setKey('')
                      invalidate()
                    }}
                  >
                    <SelectTrigger id={props.id} className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="api_key">API key</SelectItem>
                      <SelectItem value="oauth">Subscription token</SelectItem>
                    </SelectContent>
                  </Select>
                  <p className="mt-1.5 text-sm text-muted-foreground">
                    {anthropicAuth === 'api_key' ? (
                      'Create an API key in the Anthropic Console (console.anthropic.com → API keys) and paste it here.'
                    ) : (
                      <>
                        Uses your Claude Pro/Max subscription. On any machine with Claude Code installed, run{' '}
                        <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">claude setup-token</code>,
                        approve in the browser, and paste the generated token (starts with{' '}
                        <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">sk-ant-oat…</code>). The
                        token is long-lived (~1 year).
                      </>
                    )}
                  </p>
                </>
              )}
            </Field>
          )}

          {isCli && (
            <>
              <div>
                <Field label={isCursor ? 'API key' : 'Subscription token'}>
                  <Input
                    type="password"
                    value={key}
                    onChange={(e) => {
                      setKey(e.target.value)
                      invalidate()
                    }}
                    placeholder={isCursor ? 'paste key' : 'sk-ant-oat…'}
                    autoComplete="off"
                    aria-invalid={keyError != null}
                  />
                </Field>
                {keyError && (
                  <p className="mt-1.5 flex items-center gap-1.5 text-sm font-medium text-destructive">
                    <CircleAlert className="size-4 shrink-0" aria-hidden />
                    {keyError}
                  </p>
                )}
                <div className="mt-3">
                  <Field label="Credential reference">
                    <Input
                      value={ref}
                      onChange={(e) => {
                        setRef(e.target.value)
                        setRefEdited(true)
                        invalidate()
                      }}
                      placeholder={isCursor ? 'name (e.g. CURSOR_API_KEY)' : 'name (e.g. CLAUDE_CODE_TOKEN)'}
                    />
                  </Field>
                </div>
                {!keyError && <p className="mt-1.5 text-sm text-muted-foreground">{secretDestination(defaultBackend, ref)}</p>}
              </div>

              <Field label="Default model" description="used when a mission's route chain doesn't specify one">
                {(props) => (
                  <>
                    <Input
                      {...props}
                      value={cliModel}
                      onChange={(e) => {
                        setCliModel(e.target.value)
                        invalidate()
                      }}
                      placeholder={isCursor ? 'composer-2.5' : 'claude-sonnet-4-6'}
                    />
                    {!isCursor && (
                      <p className="mt-1.5 text-sm text-muted-foreground">
                        CLI aliases like sonnet, opus, or haiku also work.
                      </p>
                    )}
                  </>
                )}
              </Field>
            </>
          )}

          {!isCli && isBedrock && (
            <Field label="Region">
              {(props) => (
                <Select
                  value={region}
                  onValueChange={(v) => {
                    setRegion(v)
                    invalidate()
                  }}
                >
                  <SelectTrigger id={props.id} className="w-full">
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
              )}
            </Field>
          )}

          {!isCli && preset.id === 'custom' && (
            <Field label="Base URL">
              <Input
                value={baseURL}
                onChange={(e) => {
                  setBaseURL(e.target.value)
                  invalidate()
                }}
                placeholder="https://…/v1"
              />
            </Field>
          )}
          {!isCli && wantsKey && bedrockSplit && (
            <div>
              <BedrockKeyFields
                accessKeyId={accessKeyId}
                secretAccessKey={secretAccessKey}
                onChange={({ accessKeyId: a, secretAccessKey: s }) => {
                  setAccessKeyId(a)
                  setSecretAccessKey(s)
                  invalidate()
                }}
                errors={keyError != null}
              />
              {keyError && (
                <p className="mt-1.5 flex items-center gap-1.5 text-sm font-medium text-destructive">
                  <CircleAlert className="size-4 shrink-0" aria-hidden />
                  {keyError}
                </p>
              )}
              <div className="mt-3">
                <Field label="Credential reference">
                  <Input
                    value={ref}
                    onChange={(e) => {
                      setRef(e.target.value)
                      setRefEdited(true)
                      invalidate()
                    }}
                    placeholder="name (e.g. BEDROCK_KEYS)"
                  />
                </Field>
              </div>
            </div>
          )}
          {!isCli && wantsKey && !bedrockSplit && (
            <div>
              <CredentialField
                label={preset.id === 'custom' ? 'API key (optional)' : 'API key'}
                mode={credMode}
                onModeChange={(m) => {
                  setCredMode(m)
                  invalidate()
                }}
                existingRef={ref}
                onExistingRefChange={(v) => {
                  setRef(v)
                  setRefEdited(true)
                  invalidate()
                }}
                secretValue={key}
                onSecretValueChange={(v) => {
                  setKey(v)
                  invalidate()
                }}
                secretPlaceholder={preset.keyPlaceholder ?? 'paste key'}
                defaultBackend={defaultBackend}
                refName={ref}
                invalid={keyError != null}
              />
              {credMode === 'new' && (
                <>
                  {keyError && (
                    <p className="mt-1.5 flex items-center gap-1.5 text-sm font-medium text-destructive">
                      <CircleAlert className="size-4 shrink-0" aria-hidden />
                      {keyError}
                    </p>
                  )}
                  <div className="mt-3">
                    <Field label="Credential reference">
                      <Input
                        value={ref}
                        onChange={(e) => {
                          setRef(e.target.value)
                          setRefEdited(true)
                          invalidate()
                        }}
                        placeholder="name (e.g. OPENAI_API_KEY)"
                      />
                    </Field>
                  </div>
                  {!keyError && preset.keyHint && (
                    <p className="mt-1.5 text-sm text-muted-foreground">
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
                </>
              )}
            </div>
          )}

          {!isCli && (
            <Field label="Model" description="validated with a one-token completion, becomes the default">
              <ModelPicker
                value={model}
                onChange={(v) => {
                  setModel(v)
                  invalidate()
                }}
                suggestions={modelSuggestions}
                placeholder="model id"
              />
            </Field>
          )}

          {!isCli && !isBedrock && (
            <details className="group">
              <summary className="cursor-pointer text-sm font-medium text-muted-foreground transition hover:text-foreground">
                Advanced: base URL
              </summary>
              <div className="mt-3 grid gap-5 sm:grid-cols-2">
                <Field label="Base URL">
                  <Input
                    value={baseURL}
                    onChange={(e) => {
                      setBaseURL(e.target.value)
                      invalidate()
                    }}
                    placeholder={preset.driver === 'anthropic' ? 'https://api.anthropic.com (default)' : 'https://…/v1'}
                  />
                </Field>
              </div>
            </details>
          )}
        </FieldGroup>

        {isCli ? (
          <p className="rounded-md border border-border bg-muted/40 p-4 text-sm font-medium text-muted-foreground">
            CLI providers have no connection test.
          </p>
        ) : testState === 'gate' ? (
          <div className="flex flex-wrap items-center gap-3 rounded-md border border-border bg-muted/40 p-4 text-sm text-muted-foreground">
            <span className="min-w-0 flex-1 font-medium">Not tested yet, run a test before adding.</span>
            <Button size="sm" variant="test" disabled={busy} onClick={() => void runTest()}>
              Test connection
            </Button>
          </div>
        ) : (
          <TestStatus
            state={testState as 'testing' | 'ok' | 'failed'}
            message={
              testState === 'testing'
                ? `Sending test completion to ${model.trim() || '…'}…`
                : testState === 'ok'
                  ? `OK, ${test?.model} answered in ${test?.latency_ms} ms.${test ? responsesSuffix(test) : ''}`
                  : test
                    ? probeFailureText(test)
                    : undefined
            }
            action={
              testState !== 'testing' && (
                <Button size="sm" variant="test" disabled={busy} onClick={() => void runTest()}>
                  Test connection
                </Button>
              )
            }
          />
        )}

        <FormActions>
          <Button variant="outline" disabled={busy} onClick={() => navigate('/settings/providers')}>
            Cancel
          </Button>
          <Button disabled={(!isCli && !tested) || busy} onClick={() => void (isCli ? submitCli() : submit())}>
            Add provider
          </Button>
        </FormActions>
      </Form>
    </PageShell>
  )
}
