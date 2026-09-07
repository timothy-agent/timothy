import { Trash2 } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router'
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
import type { AdminProvider } from '../../api/types'
import { Button } from '../ui/button'
import { Switch } from '../ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../ui/table'
import { Alert, AlertDescription } from '../ui/alert'
import { ConfirmDialog } from '../timothy/confirm-dialog'
import { Field, FieldGroup, Form, FormActions } from '../timothy/field'
import { Panel } from '../timothy/panel'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { bedrockKeyJSON, BedrockKeyFields } from './BedrockKeyFields'
import { ExistingCredentialSelect } from './CredentialRefPicker'
import { catalogRowID, ModelPicker, priceLabel, type ModelSuggestion, useCatalogSearch } from './ModelPicker'
import { OptionStringField } from './OptionStringField'
import { bedrockRegions, matchPreset } from './presets'
import { ProviderLogo } from './ProviderLogo'
import { settingsArea } from './settingsAreas'
import { TestStatus } from './TestStatus'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'
import { useStagedForm } from './useStagedForm'
import { backendLabel, errText, isTimothyAuthDetail, isTimothyAuthError, probeFailureText, responsesSuffix, secretDestination, stripPaste } from './util'

const area = settingsArea('providers')

// cliModelAliases are the Claude Code CLI's own model aliases (D-051):
// a kind='cli' row has no chat driver to enumerate models against,
// so these are offered as suggestions rather than an editable
// declared-models list. 'fable' verified as a recognized alias in the
// sandbox image (unknown aliases trip a CLI warning, fable doesn't).
const cliModelAliases = ['fable', 'sonnet', 'opus', 'haiku']

// providerCatalogSearchLimit fetches this provider's whole candidate
// pool rather than the server's normal 50-row cap, since a read-only
// browsing list should show everything, not just a first page, and
// there's no "load more" affordance here (only the search box narrows
// it further).
const providerCatalogSearchLimit = 200

interface StagedProvider {
  name: string
  credential_ref: string
  reasoningDisabled: boolean
  request_timeout: string
  region: string
  litellm_provider: string
  default_model: string
}

function baselineFrom(provider: AdminProvider): StagedProvider {
  return {
    name: provider.name,
    credential_ref: provider.credential_ref,
    reasoningDisabled: provider.options?.reasoning_effort === 'none',
    request_timeout: provider.options?.request_timeout ?? '',
    region: provider.options?.region ?? 'us-east-1',
    litellm_provider: provider.options?.litellm_provider ?? '',
    default_model: provider.default_model,
  }
}

// buildPatch builds the single PATCH body from the staged values and
// the provider's own current options. It starts from the provider's
// full options map (so a key this form never shows, like
// anthropic_base_url, survives the Save) and only touches the keys
// this form stages: set when non-empty, deleted (never nulled) when
// cleared, the same rule the old per-field writes used.
export function buildPatch(provider: AdminProvider, staged: StagedProvider): Partial<AdminProvider> {
  const options: NonNullable<AdminProvider['options']> = { ...provider.options }

  if (staged.reasoningDisabled) {
    options.reasoning_effort = 'none'
  } else {
    delete options.reasoning_effort
  }

  const timeout = staged.request_timeout.trim()
  if (timeout) {
    options.request_timeout = timeout
  } else {
    delete options.request_timeout
  }

  if (provider.driver === 'bedrock') {
    options.region = staged.region
  }

  const litellmProvider = staged.litellm_provider.trim()
  if (litellmProvider) {
    options.litellm_provider = litellmProvider
  } else {
    delete options.litellm_provider
  }

  return {
    name: staged.name.trim(),
    credential_ref: staged.credential_ref,
    default_model: staged.default_model.trim(),
    options,
  }
}

// ProviderEdit loads the provider, then hands off to ProviderEditForm
// keyed by its id: a fresh mount per provider so useStagedForm's
// baseline is never initialized from a placeholder before the real
// data arrives.
export function ProviderEdit() {
  const { id } = useParams()
  const [provider, setProvider] = useState<AdminProvider | null | undefined>(undefined)

  const refresh = useCallback(() => {
    return listProviders()
      .then((list) => {
        const found = list.find((p) => p.id === id) ?? null
        setProvider(found)
        return found
      })
      .catch((err: unknown) => {
        toast.error('Could not load provider', { description: errText(err) })
        return undefined
      })
  }, [id])
  useEffect(() => {
    void refresh()
  }, [refresh])

  if (provider === null) return <Navigate to="/settings/providers" replace />
  if (provider === undefined) return null

  return <ProviderEditForm key={provider.id} initialProvider={provider} refresh={refresh} />
}

function ProviderEditForm({
  initialProvider,
  refresh,
}: {
  initialProvider: AdminProvider
  refresh: () => Promise<AdminProvider | null | undefined>
}) {
  const navigate = useNavigate()
  const defaultBackend = useDefaultSecretBackend()

  const [provider, setProviderState] = useState(initialProvider)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  const staged = useStagedForm<StagedProvider>(baselineFrom(initialProvider))
  const test = useProviderTestState()

  const doRefresh = useCallback(async () => {
    const refetched = await refresh()
    if (refetched) setProviderState(refetched)
    return refetched
  }, [refresh])

  const save = useCallback(async () => {
    setSaving(true)
    setSaveError(null)
    try {
      const patch = buildPatch(provider, staged.values)
      await patchProvider(provider.id, patch)
      toast.success('Provider saved')
      const refetched = await doRefresh()
      if (refetched) staged.rebase(baselineFrom(refetched))
    } catch (err) {
      setSaveError(errText(err))
    } finally {
      setSaving(false)
    }
  }, [provider, doRefresh, staged])

  const remove = async () => {
    try {
      await deleteProvider(provider.id)
      toast.success('Provider removed', { description: `${provider.name} no longer routes any traffic.` })
      navigate('/settings/providers')
    } catch (err) {
      toast.error('Could not remove provider', { description: errText(err) })
      setConfirmDelete(false)
    }
  }

  const runTest = async () => {
    await test.run(provider.id)
    const refetched = await doRefresh()
    if (refetched) staged.rebase(baselineFrom(refetched))
  }

  const isCli = provider.kind === 'cli'
  const isBedrock = provider.driver === 'bedrock'
  const isOpenaicompat = provider.driver === 'openaicompat'

  return (
    <PageShell width="form">
      <PageHeader
        title={provider.name}
        description={`driver: ${provider.driver}`}
        meta={<ProviderLogo preset={matchPreset(provider)} className="size-9" />}
        breadcrumbs={[
          { label: 'Settings', href: '/settings' },
          { label: area.label, href: '/settings/providers' },
          { label: provider.name },
        ]}
        actions={
          <Button variant="destructive" onClick={() => setConfirmDelete(true)}>
            <Trash2 aria-hidden />
            Delete
          </Button>
        }
      />

      <Form
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <FieldGroup>
          <Field label="Provider name">
            <Input value={staged.values.name} onChange={(e) => staged.setField('name', e.target.value)} />
          </Field>

          <Field label="Credential reference" description="Storage name for this provider's key, choose an existing one or rotate the value below.">
            <ExistingCredentialSelect
              value={staged.values.credential_ref}
              onChange={(v) => staged.setField('credential_ref', v)}
            />
          </Field>

          {isOpenaicompat && (
            <Field label="Disable reasoning" required={false}>
              {() => (
                <div className="flex items-center gap-3 text-sm">
                  <Switch
                    checked={staged.values.reasoningDisabled}
                    onCheckedChange={(v) => staged.setField('reasoningDisabled', v)}
                    aria-label="Disable reasoning"
                  />
                  <span className="text-muted-foreground">
                    Disable reasoning ("thinking") for every request to this provider.
                  </span>
                </div>
              )}
            </Field>
          )}

          {isOpenaicompat && (
            <OptionStringField
              label="Request timeout"
              description='Go duration, e.g. "20m", empty uses the default.'
              value={staged.values.request_timeout}
              onChange={(v) => staged.setField('request_timeout', v)}
              placeholder="5m"
            />
          )}

          {isBedrock && (
            <Field label="AWS region">
              {(props) => (
                <Select value={staged.values.region} onValueChange={(v) => staged.setField('region', v)}>
                  <SelectTrigger id={props.id} aria-label="AWS region" className="w-full">
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

          {!isCli && (
            <OptionStringField
              label="LiteLLM provider"
              description="Which LiteLLM provider section this provider's models are priced under. Empty infers it from driver/base URL."
              value={staged.values.litellm_provider}
              onChange={(v) => staged.setField('litellm_provider', v)}
              placeholder="e.g. xai, zai"
              mono
            />
          )}

          {isCli ? (
            <CliModelField provider={provider} value={staged.values.default_model} onChange={(v) => staged.setField('default_model', v)} />
          ) : (
            <Field label="Default model" description="Used when a route chain entry for this provider names no model.">
              <DefaultModelPicker provider={provider} value={staged.values.default_model} onChange={(v) => staged.setField('default_model', v)} />
            </Field>
          )}
        </FieldGroup>

        {saveError && (
          <Alert tone="destructive">
            <AlertDescription className="flex flex-wrap items-center justify-between gap-3">
              <span>Could not save provider changes: {saveError}</span>
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

      {!isCli && (
        <div className="mt-10">
          <Panel title={isBedrock ? 'AWS credentials' : 'API key'}>
            <CredentialPanel
              provider={provider}
              defaultBackend={defaultBackend}
              bedrock={isBedrock}
              onChanged={() => {
                void doRefresh().then((refetched) => {
                  if (refetched) staged.rebase(baselineFrom(refetched))
                })
              }}
            />
          </Panel>
        </div>
      )}

      <div className="mt-10 space-y-4">
        {isCli ? (
          <p className="rounded-md border border-border bg-muted/40 p-4 text-sm text-muted-foreground">
            Health reflects the last harness run, not a live probe. Subscription auth has no chat
            endpoint to test.
          </p>
        ) : test.state === 'idle' ? (
          <div className="flex flex-wrap items-center gap-3 rounded-md border border-border bg-muted/40 p-4 text-sm text-muted-foreground">
            <span className="min-w-0 flex-1 font-medium">Not tested yet.</span>
            <Button size="sm" variant="test" onClick={() => void runTest()}>
              Test connection
            </Button>
          </div>
        ) : (
          <TestStatus
            state={test.state}
            message={test.message}
            detail={test.detail}
            action={
              test.state !== 'testing' && (
                <Button size="sm" variant="test" onClick={() => void runTest()}>
                  Test connection
                </Button>
              )
            }
          />
        )}
      </div>

      {!isCli && (
        <div className="mt-10">
          <ProviderCatalogPanel provider={provider} />
        </div>
      )}

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete ${provider.name}?`}
        description="Removes the provider row and its models. Refused while an enabled route still points at it."
        confirmLabel="Delete"
        destructive
        onConfirm={() => void remove()}
      />
    </PageShell>
  )
}

// useProviderTestState mirrors useProviderTest but takes the provider
// id at call time (run(id)) rather than at hook creation, since
// ProviderEdit's provider row changes identity on every refresh (a new
// object from listProviders).
function useProviderTestState() {
  const [state, setState] = useState<{ state: 'idle' | 'testing' | 'ok' | 'failed'; message?: string; detail?: string }>({
    state: 'idle',
  })

  const run = useCallback(async (id: string) => {
    setState({ state: 'testing' })
    try {
      const res = await testProvider(id)
      if (!res.ok && isTimothyAuthDetail(res.detail)) {
        setState({ state: 'idle' })
        return
      }
      if (res.ok) {
        setState({ state: 'ok', message: `OK, ${res.model} answered in ${res.latency_ms} ms.${responsesSuffix(res)}` })
      } else {
        setState({ state: 'failed', message: probeFailureText(res), detail: res.detail })
      }
    } catch (err) {
      if (isTimothyAuthError(err)) {
        setState({ state: 'idle' })
        return
      }
      const detail = err instanceof Error ? err.message : String(err)
      setState({ state: 'failed', message: probeFailureText({ latency_ms: 0, detail }), detail })
    }
  }, [])

  return { ...state, run }
}

function CredentialPanel({
  provider,
  defaultBackend,
  bedrock,
  onChanged,
}: {
  provider: AdminProvider
  defaultBackend?: string
  bedrock?: boolean
  onChanged: () => void
}) {
  const [configured, setConfigured] = useState(false)
  const [storedBackend, setStoredBackend] = useState('')
  const [secretValue, setSecretValue] = useState('')
  const [accessKeyId, setAccessKeyId] = useState('')
  const [secretAccessKey, setSecretAccessKey] = useState('')
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

  const saveSecretValue = async () => {
    const ref = provider.credential_ref
    if (!ref) return
    if (bedrock) {
      if (!accessKeyId.trim() || !secretAccessKey.trim()) return
    } else if (!secretValue) {
      return
    }
    setSavingSecret(true)
    try {
      await setSecret(ref, bedrock ? bedrockKeyJSON(accessKeyId, secretAccessKey) : stripPaste(secretValue))
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
    const ref = provider.credential_ref
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

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <span className={`rounded-md px-2 py-0.5 text-xs font-semibold uppercase ${configured ? 'bg-good-soft text-good' : 'bg-warning-soft text-warning'}`}>
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
          <BedrockKeyFields
            accessKeyId={accessKeyId}
            secretAccessKey={secretAccessKey}
            onChange={({ accessKeyId: a, secretAccessKey: s }) => {
              setAccessKeyId(a)
              setSecretAccessKey(s)
            }}
          />
          <Button
            variant="outline"
            disabled={savingSecret || !accessKeyId.trim() || !secretAccessKey.trim() || !provider.credential_ref}
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
            autoComplete="off"
          />
          <Button variant="outline" disabled={savingSecret || !secretValue || !provider.credential_ref} onClick={() => void saveSecretValue()}>
            Save
          </Button>
        </div>
      )}
      {!bedrock && <p className="text-sm text-muted-foreground">{secretDestination(defaultBackend ?? 'db', provider.credential_ref)}</p>}
    </div>
  )
}

function CliModelField({
  provider,
  value,
  onChange,
}: {
  provider: AdminProvider
  value: string
  onChange: (v: string) => void
}) {
  // Cursor has its own model slug namespace (not the Claude Code CLI's
  // aliases, and not filed under any catalog provider), so its picker
  // gets no alias pinning and no catalog-backed suggestions. Instead it
  // fetches Cursor's own live model list.
  const isCursor = provider.driver === 'cursor-cli'

  const search = useCallback((q: string) => catalogModelsForProvider(provider.id, q), [provider.id])
  const catalogModels = useCatalogSearch(value, search)

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
        if (!cancelled) setCursorModels(models.map((m) => ({ id: m.id, name: m.display_name })))
      },
      () => {
        if (!cancelled) setCursorModels([])
      },
    )
    return () => {
      cancelled = true
    }
  }, [isCursor, provider.id])

  const suggestions: ModelSuggestion[] = useMemo(() => {
    if (isCursor) return cursorModels
    const seen = new Map<string, ModelSuggestion>(cliModelAliases.map((a) => [a, { id: a }]))
    for (const m of catalogModels) {
      const id = catalogRowID(m)
      if (!seen.has(id)) {
        seen.set(id, { id, input_per_mtok: m.input_per_mtok, output_per_mtok: m.output_per_mtok })
      }
    }
    return [...seen.values()]
  }, [catalogModels, isCursor, cursorModels])

  return (
    <Field
      label="Default model"
      description={
        isCursor
          ? 'Subscription auth has no declared model list. This sets the default model the CLI runs.'
          : "Subscription auth uses the Claude Code CLI's own model aliases, not a declared list. This sets the default model the CLI runs."
      }
    >
      <ModelPicker
        value={value}
        onChange={onChange}
        suggestions={suggestions}
        placeholder={isCursor ? 'composer-2.5' : 'sonnet'}
      />
    </Field>
  )
}

function DefaultModelPicker({
  provider,
  value,
  onChange,
}: {
  provider: AdminProvider
  value: string
  onChange: (v: string) => void
}) {
  const search = useCallback((q: string) => catalogModelsForProvider(provider.id, q), [provider.id])
  const catalogModels = useCatalogSearch(value, search)

  const suggestions: ModelSuggestion[] = useMemo(
    () => catalogModels.map((m) => ({ id: catalogRowID(m), input_per_mtok: m.input_per_mtok, output_per_mtok: m.output_per_mtok })),
    [catalogModels],
  )

  return <ModelPicker value={value} onChange={onChange} suggestions={suggestions} placeholder="model id" />
}

// ProviderCatalogPanel is a read-only, searchable table of every
// catalog model within this provider's candidate litellm_provider(s).
// Model *selection* for actual use lives solely on the Routes page;
// this is purely "what's available and what it costs."
function ProviderCatalogPanel({ provider }: { provider: AdminProvider }) {
  const [q, setQ] = useState('')
  const search = useCallback(
    (query: string) => catalogModelsForProvider(provider.id, query, providerCatalogSearchLimit),
    [provider.id],
  )
  const models = useCatalogSearch(q, search)

  return (
    <Panel title={`Models · ${models.length}`} description="Every catalog model this provider can serve, with live pricing. Pick which one actually runs on the Routes page." density="operational">
      <div className="p-3">
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="filter by model id…" size="sm" />
      </div>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Model</TableHead>
            <TableHead>Context</TableHead>
            <TableHead className="text-right">Price</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {models.map((m) => {
            const id = catalogRowID(m)
            return (
              <TableRow key={m.model_key}>
                <TableCell className="font-mono">{id}</TableCell>
                <TableCell className="text-muted-foreground">
                  {m.max_input_tokens != null ? `${Math.round(m.max_input_tokens / 1000)}k ctx` : ''}
                </TableCell>
                <TableCell className="text-right font-mono text-xs text-muted-foreground">
                  {priceLabel({ id, input_per_mtok: m.input_per_mtok, output_per_mtok: m.output_per_mtok })}
                </TableCell>
              </TableRow>
            )
          })}
          {models.length === 0 && (
            <TableRow>
              <TableCell colSpan={3} className="text-muted-foreground">
                no catalog models found
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
    </Panel>
  )
}
