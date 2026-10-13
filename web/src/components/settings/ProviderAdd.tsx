import { CircleAlert } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import {
  createProvider,
  listSecretRefs,
  providerOAuthStart,
  searchCatalog,
  setSecret,
  subscriptionAuthCallback,
  subscriptionAuthCancel,
  subscriptionAuthStart,
  subscriptionAuthStatus,
  validateProvider,
  type SecretRefEntry,
} from '../../api/client'
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
import { useOnboarding } from '../../onboarding/context'

const area = settingsArea('providers')

// refFor derives a credential ref for a named provider instance: the
// preset's conventional storage-key name while nothing references it
// yet, otherwise one from the user's name, so a second Cursor or
// OpenAI account never lands on the first one's ref.
function refFor(preset: ProviderPreset, name: string, taken: ReadonlySet<string>): string {
  if (preset.defaultRef && !taken.has(preset.defaultRef)) return preset.defaultRef
  const slug = name.trim().toUpperCase().replace(/[^A-Z0-9]+/g, '_')
  return slug ? `${slug}_API_KEY` : ''
}

// referentsOf lists what already reads the ref, the things a fresh
// secret write under that name would break.
function referentsOf(refs: SecretRefEntry[], ref: string): string[] {
  return (refs.find((r) => r.name === ref)?.referenced_by ?? []).map((r) => r.name)
}

// presetLitellmProvider maps a preset id to the litellm_provider value
// its models are filed under in the synced catalog, mirroring
// catalog.CandidateProviders' driver/host mapping for this fixed set
// of presets. '' means no restriction, same as an unrecognized
// driver/host server-side.
function presetLitellmProvider(presetId: string): string {
  return {
    openai: 'openai',
    chatgpt: 'openai',
    anthropic: 'anthropic',
    claude: 'anthropic',
    bedrock: 'bedrock',
    glm: 'zai',
    grok: 'xai',
    ollama: 'ollama',
  }[presetId] ?? ''
}

// BedrockAuthMode is the Bedrock preset's credential source (D-156):
// an IAM access key pair stored as a secret (default) or the cloud
// identity the gateway runs under, which stores nothing.
type BedrockAuthMode = 'static' | 'ambient'

interface ProviderAddProps {
  // presetId overrides the URL param.
  presetId?: string
  // embedded renders the form alone, without the page chrome.
  embedded?: boolean
  // onCreated and onCancel replace the navigation back to the list.
  onCreated?: (id: string, name: string) => void
  onCancel?: () => void
}

// ProviderAdd is its own page (not a dialog): connecting a provider is
// a create action, and validation runs a real one-token completion
// against the unsaved config, a provider is born working or not at
// all. The Add button stays disabled until that test has passed;
// editing any field after a passing test re-locks it, since the
// config it validated no longer matches what's on screen.
export function ProviderAdd({ presetId: presetIdProp, embedded, onCreated, onCancel }: ProviderAddProps = {}) {
  const params = useParams()
  const presetId = presetIdProp ?? params.presetId
  const navigate = useNavigate()
  const { refresh: refreshOnboarding } = useOnboarding()
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
  const [bedrockAuth, setBedrockAuth] = useState<BedrockAuthMode>('static')
  // credMode picks between typing a new secret (default) and reusing a
  // stored ref, only offered for the plain (non-bedrock-split,
  // non-CLI) API key flow, the common reuse case (e.g. the same
  // OpenAI-compatible key across two provider rows).
  const [credMode, setCredMode] = useState<CredentialMode>('new')
  // pasteKey switches an oauth preset from Connect to the plain paste
  // flow for operators who already hold a key.
  const [pasteKey, setPasteKey] = useState(false)
  // secretRefs: the stored-ref directory, for steering the derived
  // ref away from names other providers use and refusing to overwrite
  // one on submit.
  const [secretRefs, setSecretRefs] = useState<SecretRefEntry[]>([])
  useEffect(() => {
    let cancelled = false
    listSecretRefs().then(
      (refs) => {
        if (!cancelled) setSecretRefs(refs)
      },
      () => {},
    )
    return () => {
      cancelled = true
    }
  }, [])
  const takenRefs = useMemo(
    () => new Set(secretRefs.filter((r) => r.referenced_by.length > 0).map((r) => r.name)),
    [secretRefs],
  )

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
    setBedrockAuth('static')
    setKey('')
    setAccessKeyId('')
    setSecretAccessKey('')
    setRefEdited(false)
    setModel(preset.validateModel)
    setCliModel(preset.id === 'cursor' ? 'composer-2.5' : 'claude-sonnet-4-6')
    setBusy(false)
    setKeyError(null)
    setTest(null)
    setTested(false)
    setCredMode('new')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [preset?.id])

  // The derived ref follows the name (and the stored-ref directory as
  // it loads) until the user edits the ref field by hand.
  useEffect(() => {
    if (!preset || refEdited) return
    setRef(refFor(preset, name, takenRefs))
  }, [preset, name, refEdited, takenRefs])

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

  if (preset.oauth && !pasteKey) {
    const panel = (
      <OAuthConnectPanel
        returnTo={embedded ? 'wizard' : 'settings'}
        onPasteKey={() => setPasteKey(true)}
        onCancel={embedded ? onCancel : () => navigate('/settings/providers')}
      />
    )
    if (embedded) return panel
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
        {panel}
      </PageShell>
    )
  }

  const isSubscriptionPreset = preset.id === 'chatgpt' || preset.id === 'claude'

  if (isSubscriptionPreset && !pasteKey) {
    const panel = (
      <SubscriptionConnectPanel
        preset={preset}
        onConnected={(id, pName) => {
          void refreshOnboarding()
          if (onCreated) onCreated(id, pName)
          else navigate('/settings/providers')
        }}
        onManualConfig={() => setPasteKey(true)}
        onCancel={embedded ? onCancel : () => navigate('/settings/providers')}
      />
    )
    if (embedded) return panel
    return (
      <PageShell width="form">
        <PageHeader
          title={`Connect ${preset.name}`}
          description={`subscription: ${preset.id === 'chatgpt' ? 'OpenAI ChatGPT' : 'Anthropic Claude'}`}
          meta={<ProviderLogo preset={preset} className="size-9" />}
          breadcrumbs={[
            { label: 'Settings', href: '/settings' },
            { label: area.label, href: '/settings/providers' },
            { label: `Connect ${preset.name}` },
          ]}
        />
        {panel}
      </PageShell>
    )
  }

  const isBedrock = preset.driver === 'bedrock'
  const isAnthropic = preset.id === 'anthropic'
  const isCursor = preset.id === 'cursor'
  // isCli: Cursor creates a kind='cli' row (D-051) for coding missions.
  const isCli = isCursor
  // bedrockAmbient: the gateway's own cloud identity signs requests,
  // so the row carries no secret and no reference (D-156).
  const bedrockAmbient = isBedrock && bedrockAuth === 'ambient'
  const wantsKey = preset.requiresKey && !bedrockAmbient
  // A keyless preset (Ollama, ChatGPT/Claude subscription) stores no secret, so it must not name a
  // reference either: the gateway treats an unresolved ref as unhealthy.
  const credentialRef = wantsKey ? ref.trim() : ''
  const bedrockOptions = isBedrock ? { region, ...(bedrockAmbient ? { auth: 'ambient' } : {}) } : {}

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

  // refConflict refuses a new secret write under a ref something else
  // already reads: the store is an upsert, so the write would silently
  // replace that provider's key.
  const refConflict = (): boolean => {
    const users = referentsOf(secretRefs, ref.trim())
    if (users.length === 0) return false
    setKeyError(`${ref.trim()} already holds the key for ${users.join(', ')}. Pick another reference name.`)
    return true
  }

  // submitCli validates the pasted subscription token, stores it, and
  // creates the kind='cli' row. No probe: no chat driver exists for
  // these rows (D-051), so there is nothing to test against. Cursor's API
  // key has no fixed prefix to check, so any non-empty value passes.
  const submitCli = async () => {
    if (!name.trim()) {
      toast.error('Name required', { description: 'Give this provider a unique name before adding.' })
      return
    }
    const trimmedKey = stripPaste(key)
    if (!trimmedKey) {
      setKeyError('An API key is required.')
      return
    }
    if (!ref.trim()) {
      setKeyError('a credential reference name is required to store it')
      return
    }
    if (refConflict()) return
    setKeyError(null)
    setBusy(true)
    try {
      await setSecret(ref.trim(), trimmedKey)
      const id = await createProvider({
        name: name.trim(),
        kind: 'cli',
        driver: preset.driver,
        base_url: '',
        credential_ref: ref.trim(),
        headers: {},
        default_model: cliModel.trim(),
        enabled: true,
      })
      toast.success('Provider added', { description: `${name.trim()} is ready for coding missions.` })
      void refreshOnboarding()
      if (onCreated) onCreated(id, name.trim())
      else navigate('/settings/providers')
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
    if (!usingExistingCred && isAnthropic && stripPaste(key).startsWith('sk-ant-oat')) {
      setKeyError('This looks like a subscription token. Add the "Claude Subscription" provider preset instead.')
      return
    }
    if (!name.trim()) {
      setKeyError(null)
      toast.error('Name required', { description: 'Give this provider a unique name before testing.' })
      return
    }
    if (wantsKey && hasKey && !usingExistingCred && refConflict()) return
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
        credential_ref: credentialRef,
        headers: {},
        ...(isBedrock ? { options: bedrockOptions } : {}),
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
        ...bedrockOptions,
        ...(litellmProvider ? { litellm_provider: litellmProvider } : {}),
      }
      const id = await createProvider({
        name: name.trim(),
        kind: 'api',
        driver: preset.driver,
        base_url: baseURL.trim(),
        credential_ref: credentialRef,
        headers: {},
        default_model: trimmedModel,
        enabled: true,
        ...(Object.keys(options).length > 0 ? { options } : {}),
      })
      toast.success('Provider added', { description: `${name.trim()} is connected and ready to use.` })
      void refreshOnboarding()
      if (onCreated) onCreated(id, name.trim())
      else navigate('/settings/providers')
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

  const form = (
    <>
      {preset.id === 'ollama' && (
        <p className="mb-4 text-sm text-muted-foreground">
          Same machine as Timothy? Keep the default address. Another machine? Enter its address, for example
          http://192.168.1.20:11434/v1.
        </p>
      )}
      {preset.id === 'chatgpt' && (
        <div className="mb-4 rounded-md border border-border/60 bg-muted/30 p-3 text-sm text-muted-foreground">
          <p className="font-medium text-foreground">Requires ChatGPT Plus or Pro subscription</p>
          <p className="mt-1">
            Authenticate once with your OpenAI account by running on your host:
          </p>
          <code className="mt-2 block rounded bg-muted px-2 py-1 font-mono text-xs text-foreground">
            docker exec -it timothy-cliproxy ./CLIProxyAPI -codex-device-login
          </code>
          <p className="mt-1.5 text-xs">
            Open the displayed OpenAI link, sign in, and enter the one-time device code. Once authenticated, click Test below.
          </p>
        </div>
      )}
      {preset.id === 'claude' && (
        <div className="mb-4 rounded-md border border-border/60 bg-muted/30 p-3 text-sm text-muted-foreground">
          <p className="font-medium text-foreground">Requires Claude Pro or Team subscription</p>
          <p className="mt-1">
            Authenticate once with your Anthropic account by running on your host:
          </p>
          <code className="mt-2 block rounded bg-muted px-2 py-1 font-mono text-xs text-foreground">
            docker exec -it timothy-cliproxy ./CLIProxyAPI -claude-login
          </code>
          <p className="mt-1.5 text-xs">
            Approve the OAuth prompt in your browser. Once authenticated, click Test below.
          </p>
        </div>
      )}
      <Form onSubmit={(e) => e.preventDefault()}>
        <FieldGroup>
          <Field label="Name (unique)">
            <Input
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                invalidate()
              }}
              placeholder={preset.id === 'custom' ? 'my-gateway' : preset.name}
            />
          </Field>

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
            <Field label="Authentication">
              {(props) => (
                <>
                  <Select
                    value={bedrockAuth}
                    onValueChange={(v) => {
                      setBedrockAuth(v as BedrockAuthMode)
                      setAccessKeyId('')
                      setSecretAccessKey('')
                      invalidate()
                    }}
                  >
                    <SelectTrigger id={props.id} className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="static">Access keys</SelectItem>
                      <SelectItem value="ambient">Cloud identity</SelectItem>
                    </SelectContent>
                  </Select>
                  <p className="mt-1.5 text-sm text-muted-foreground">
                    {bedrockAuth === 'static'
                      ? 'An IAM access key pair, stored in the secret store.'
                      : 'The AWS role attached to the gateway (EKS Pod Identity, IRSA, GKE or AKS Workload Identity, or an instance role). Timothy stores no key.'}
                  </p>
                </>
              )}
            </Field>
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
        {!isCli && testState === 'failed' && baseURL.includes('host.docker.internal') && (
          <p className="text-sm text-muted-foreground">
            On a Linux server, Docker needs{' '}
            <code className="rounded-md bg-muted px-1 py-0.5 font-mono text-xs">
              extra_hosts: ["host.docker.internal:host-gateway"]
            </code>{' '}
            on the gateway service for this address to resolve.
          </p>
        )}

        <FormActions>
          <Button variant="outline" disabled={busy} onClick={() => (onCancel ? onCancel() : navigate('/settings/providers'))}>
            Cancel
          </Button>
          <Button disabled={(!isCli && !tested) || busy} onClick={() => void (isCli ? submitCli() : submit())}>
            Add provider
          </Button>
        </FormActions>
      </Form>
    </>
  )

  if (embedded) return form

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
      {form}
    </PageShell>
  )
}

// OAuthConnectPanel is the Connect flow for an oauth preset (D-163):
// the browser logs in at the provider, the key is minted and stored
// server-side, and the callback brings the operator back here.
function OAuthConnectPanel({
  returnTo,
  onPasteKey,
  onCancel,
}: {
  returnTo: 'wizard' | 'settings'
  onPasteKey: () => void
  onCancel?: () => void
}) {
  const [busy, setBusy] = useState(false)
  const connect = async () => {
    setBusy(true)
    try {
      window.location.assign(await providerOAuthStart(returnTo))
    } catch (err) {
      toast.error('Could not start the login', { description: errText(err) })
      setBusy(false)
    }
  }
  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">
        Log in or sign up at OpenRouter and approve Timothy. OpenRouter creates an API key for Timothy and
        stores it here. You never paste anything.
      </p>
      <ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground">
        <li>Free models cost nothing: 50 requests a day, or 1000 a day after you buy 10 credits once.</li>
        <li>Paid models on the same account use your OpenRouter credits.</li>
        <li>Your OpenRouter privacy settings decide whether free models may train on your prompts.</li>
      </ul>
      <FormActions>
        <Button onClick={() => void connect()} disabled={busy}>
          {busy ? 'Opening OpenRouter…' : 'Connect OpenRouter'}
        </Button>
        <Button variant="ghost" onClick={onPasteKey} disabled={busy}>
          I have a key, paste it instead
        </Button>
        {onCancel && (
          <Button variant="ghost" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
        )}
      </FormActions>
    </div>
  )
}

function SubscriptionConnectPanel({
  preset,
  onConnected,
  onManualConfig,
  onCancel,
}: {
  preset: ProviderPreset
  onConnected: (id: string, name: string) => void
  onManualConfig: () => void
  onCancel?: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [session, setSession] = useState<{ url: string; state: string } | null>(null)
  const [elapsed, setElapsed] = useState(0)
  const [redirectUrlInput, setRedirectUrlInput] = useState('')
  const [submittingManual, setSubmittingManual] = useState(false)
  const [statusError, setStatusError] = useState<string | null>(null)

  const providerType = preset.id === 'chatgpt' ? 'chatgpt' : 'claude'

  const startConnect = async () => {
    setBusy(true)
    setStatusError(null)
    setElapsed(0)
    try {
      const res = await subscriptionAuthStart(providerType)
      setSession({ url: res.url, state: res.state })
      window.open(res.url, '_blank', 'noopener,noreferrer')
    } catch (err) {
      toast.error('Could not start login flow', { description: errText(err) })
      setBusy(false)
    }
  }

  useEffect(() => {
    if (!session) return
    const timer = setInterval(() => {
      setElapsed((s) => s + 1)
    }, 1000)
    return () => clearInterval(timer)
  }, [session])

  useEffect(() => {
    if (!session) return
    let active = true

    const poll = async () => {
      try {
        const res = await subscriptionAuthStatus(session.state)
        if (!active) return
        if (res.status === 'ok') {
          try {
            const id = await createProvider({
              name: preset.name,
              kind: 'api',
              driver: 'openaicompat',
              base_url: 'http://cliproxy:8317/v1',
              credential_ref: 'local',
              headers: {},
              default_model: preset.validateModel || (providerType === 'chatgpt' ? 'gpt-4o' : 'claude-3-7-sonnet-20250219'),
              enabled: true,
            })
            toast.success('Connected', { description: `${preset.name} is ready to use.` })
            onConnected(id, preset.name)
          } catch (createErr) {
            toast.error('Authenticated, but failed to create provider', { description: errText(createErr) })
            setBusy(false)
            setSession(null)
          }
          return
        }
        if (res.status === 'error') {
          setStatusError(res.error || 'Authentication failed or was cancelled')
          setBusy(false)
          setSession(null)
          return
        }
      } catch {
        // Retry next interval
      }
    }

    void poll()
    const interval = setInterval(poll, 2000)
    return () => {
      active = false
      clearInterval(interval)
    }
  }, [session, preset, providerType, onConnected])

  const cancelSession = async () => {
    if (session) {
      try {
        await subscriptionAuthCancel(session.state)
      } catch {
        // Best-effort cancellation
      }
    }
    setSession(null)
    setBusy(false)
    setStatusError(null)
  }

  const submitManualCallback = async () => {
    if (!redirectUrlInput.trim()) return
    setSubmittingManual(true)
    try {
      const res = await subscriptionAuthCallback(providerType, redirectUrlInput.trim())
      if (res.status === 'ok') {
        toast.info('Callback submitted, verifying session…')
      } else {
        toast.error('Callback error', { description: res.error || 'Invalid callback URL' })
      }
    } catch (err) {
      toast.error('Could not submit callback', { description: errText(err) })
    } finally {
      setSubmittingManual(false)
    }
  }

  if (session) {
    return (
      <div className="space-y-6 rounded-lg border border-border bg-card p-6 shadow-sm">
        <div className="space-y-2">
          <h3 className="text-base font-semibold text-foreground">Waiting for authorization in your browser…</h3>
          <p className="text-sm text-muted-foreground">
            A new browser tab opened to sign into {providerType === 'chatgpt' ? 'OpenAI' : 'Anthropic'}. Approve the access request there to continue.
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-3">
          <Button asChild variant="outline" size="sm">
            <a href={session.url} target="_blank" rel="noopener noreferrer">
              Re-open login page
            </a>
          </Button>
          <Button variant="ghost" size="sm" onClick={() => void cancelSession()}>
            Cancel
          </Button>
          <span className="text-xs text-muted-foreground">Elapsed: {elapsed}s</span>
        </div>

        {elapsed > 8 && (
          <div className="rounded-md border border-border/70 bg-muted/30 p-4 text-xs space-y-2">
            <p className="font-medium text-foreground">Running on a remote server or headless host?</p>
            <p className="text-muted-foreground">
              If your browser says &ldquo;Unable to connect&rdquo; to localhost after logging in, copy the full URL from your browser&apos;s address bar and paste it below:
            </p>
            <div className="flex gap-2">
              <Input
                placeholder="http://localhost:1455/auth/callback?code=..."
                value={redirectUrlInput}
                onChange={(e) => setRedirectUrlInput(e.target.value)}
                className="text-xs font-mono"
              />
              <Button size="sm" disabled={submittingManual || !redirectUrlInput.trim()} onClick={() => void submitManualCallback()}>
                {submittingManual ? 'Submitting…' : 'Submit'}
              </Button>
            </div>
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {statusError && (
        <div className="flex items-center gap-2 rounded-md bg-destructive/10 p-3 text-sm text-destructive">
          <CircleAlert className="size-4 shrink-0" aria-hidden />
          <span>{statusError}</span>
        </div>
      )}

      <p className="text-sm text-muted-foreground">
        {providerType === 'chatgpt'
          ? 'Sign in with your ChatGPT Plus or Pro subscription. Timothy routes inference through your existing flat-rate account with zero per-token cost.'
          : 'Sign in with your Claude Pro or Team subscription. Timothy routes inference through your existing flat-rate account with zero per-token cost.'}
      </p>

      <ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground">
        <li>Flat-rate subscription models cost $0.00 on your Timothy ledger.</li>
        <li>Sessions are managed securely by CLIProxyAPI inside your Docker network.</li>
        <li>No API keys to paste, generate, or renew manually.</li>
      </ul>

      <FormActions>
        <Button onClick={() => void startConnect()} disabled={busy}>
          {busy ? 'Opening login page…' : providerType === 'chatgpt' ? 'Sign in with ChatGPT' : 'Sign in with Claude'}
        </Button>
        <Button variant="ghost" onClick={onManualConfig} disabled={busy}>
          Configure endpoint manually
        </Button>
        {onCancel && (
          <Button variant="ghost" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
        )}
      </FormActions>
    </div>
  )
}

