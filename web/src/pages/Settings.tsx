import { ArrowDown01Icon, ArrowUp01Icon, Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import {
  createProvider,
  deleteProvider,
  deleteSecret,
  deleteSecretBackendConfig,
  getSecretBackendConfig,
  getSettings,
  listProviders,
  listRoutes,
  patchBudget,
  patchProvider,
  patchRoute,
  patchSettings,
  providersHealth,
  putSecretBackendConfig,
  secretStatus,
  setSecret,
  setSecretExternal,
  testProvider,
  testSecretBackend,
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../components/ui/select'

const tabs = ['Providers', 'Task allocation', 'Secrets', 'Features'] as const

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
        {tab === 'Secrets' && <SecretsTab />}
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

// backendLabel names a secret's storage in UI copy.
const backendLabels: Record<string, string> = { db: 'encrypted', vault: 'vault', asm: 'aws' }
function backendLabel(b: string): string {
  return backendLabels[b] ?? b
}

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
  const [configured, setConfigured] = useState(false)
  const [storedBackend, setStoredBackend] = useState('')
  const [source, setSource] = useState<'db' | 'vault' | 'asm'>('db')
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

  const saveSecretValue = async () => {
    if (!ref || !secretValue) return
    setSavingSecret(true)
    try {
      if (source === 'db') await setSecret(ref, secretValue)
      else await setSecretExternal(ref, source, secretValue)
      setSecretValue('')
      refreshSecretStatus()
      onChanged()
    } catch (err) {
      onError(err instanceof Error ? err.message : String(err))
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
      onError(err instanceof Error ? err.message : String(err))
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
              <Select value={source} onValueChange={(v) => setSource(v as 'db' | 'vault' | 'asm')}>
                <SelectTrigger className="h-8 w-40 shrink-0 text-xs" aria-label="key source">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="db">Encrypted here</SelectItem>
                  <SelectItem value="vault">Vault</SelectItem>
                  <SelectItem value="asm">AWS Secrets Manager</SelectItem>
                </SelectContent>
              </Select>
              <Input
                type={source === 'db' ? 'password' : 'text'}
                value={secretValue}
                onChange={(e) => setSecretValue(e.target.value)}
                placeholder={
                  source === 'db'
                    ? configured
                      ? 'paste new key to rotate'
                      : 'paste key'
                    : source === 'vault'
                      ? 'path, e.g. timothy/anthropic#api_key'
                      : 'name or ARN, optional #json_key'
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
              {source === 'db'
                ? 'Encrypted with the master key and kept in Timothy’s database.'
                : source === 'vault'
                  ? 'The key stays in Vault; only its path is saved. Connect Vault in the Secrets tab.'
                  : 'The key stays in AWS Secrets Manager; only its name is saved. Connect AWS in the Secrets tab.'}
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

// --- Secrets tab ---

// useBackendCard holds the shared load/save/test/remove machinery for
// one external secret backend's card.
function useBackendCard(
  backend: 'vault' | 'asm',
  onLoaded: (cfg: Record<string, string>) => void,
  onError: (msg: string) => void,
) {
  const [configured, setConfigured] = useState(false)
  const [busy, setBusy] = useState(false)
  const [status, setStatus] = useState('')

  // Ref keeps the load effect stable even though onLoaded is a fresh
  // closure every render.
  const onLoadedRef = useRef(onLoaded)
  onLoadedRef.current = onLoaded
  useEffect(() => {
    getSecretBackendConfig(backend).then((c) => {
      setConfigured(Object.keys(c).length > 0)
      onLoadedRef.current(c)
    }, () => undefined)
  }, [backend])

  const run = (fn: () => Promise<string>) => {
    setBusy(true)
    fn()
      .then(setStatus)
      .catch((err: unknown) => onError(err instanceof Error ? err.message : String(err)))
      .finally(() => setBusy(false))
  }

  const test = () =>
    run(async () => {
      const res = await testSecretBackend(backend)
      return res.ok ? 'connection OK' : `failed: ${res.error ?? 'unknown'}`
    })

  const remove = () =>
    run(async () => {
      await deleteSecretBackendConfig(backend)
      setConfigured(false)
      return 'removed'
    })

  return { configured, setConfigured, busy, status, run, test, remove }
}

function BackendCardHeader({
  title,
  subtitle,
  configured,
  status,
  busy,
  canSave,
  onSave,
  onTest,
  onRemove,
}: {
  title: string
  subtitle: string
  configured: boolean
  status: string
  busy: boolean
  canSave: boolean
  onSave: () => void
  onTest: () => void
  onRemove: () => void
}) {
  return (
    <>
      <div className="flex flex-wrap items-center gap-3">
        <span className="font-medium">{title}</span>
        <span
          className={`rounded px-1.5 py-px text-[10px] font-medium uppercase ${
            configured
              ? 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400'
              : 'bg-zinc-100 text-zinc-500 dark:bg-zinc-800 dark:text-zinc-400'
          }`}
        >
          {configured ? 'configured' : 'not configured'}
        </span>
        {status && (
          <span
            className={`text-xs ${status.startsWith('failed') ? 'text-red-600 dark:text-red-400' : 'text-emerald-600 dark:text-emerald-400'}`}
          >
            {status}
          </span>
        )}
        <div className="ml-auto flex items-center gap-3">
          <Button size="sm" variant="outline" disabled={busy || !configured} onClick={onTest}>
            Test
          </Button>
          <Button size="sm" variant="outline" disabled={busy || !canSave} onClick={onSave}>
            Save
          </Button>
          {configured && (
            <button
              type="button"
              aria-label={`Remove ${title}`}
              disabled={busy}
              onClick={onRemove}
              className="text-muted-foreground hover:text-red-500"
            >
              <HugeiconsIcon icon={Delete02Icon} className="size-4" />
            </button>
          )}
        </div>
      </div>
      <p className="mt-1 text-xs text-muted-foreground">{subtitle}</p>
    </>
  )
}

function Field({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <label className="text-xs text-muted-foreground">
      {label}
      {children}
    </label>
  )
}

function VaultCard({ onError }: { onError: (msg: string) => void }) {
  const [cfg, setCfg] = useState({
    address: '',
    mount: '',
    auth: 'token',
    token_ref: '',
    role_id: '',
    secret_id_ref: '',
  })
  const [tokenPaste, setTokenPaste] = useState('')
  const [secretIDPaste, setSecretIDPaste] = useState('')
  const card = useBackendCard(
    'vault',
    (c) => setCfg((v) => ({ ...v, ...c, auth: c.auth || 'token' })),
    onError,
  )

  const save = () =>
    card.run(async () => {
      await putSecretBackendConfig('vault', cfg)
      if (cfg.auth === 'token' && tokenPaste) {
        await setSecret(cfg.token_ref || 'VAULT_TOKEN', tokenPaste)
        setTokenPaste('')
      }
      if (cfg.auth === 'approle' && secretIDPaste) {
        await setSecret(cfg.secret_id_ref || 'VAULT_SECRET_ID', secretIDPaste)
        setSecretIDPaste('')
      }
      card.setConfigured(true)
      return 'saved'
    })

  return (
    <div className="rounded-xl border border-border p-4">
      <BackendCardHeader
        title="HashiCorp Vault"
        subtitle="KV v2 mount. Credentials pasted here are kept in the encrypted store, never in this config."
        configured={card.configured}
        status={card.status}
        busy={card.busy}
        canSave={!!cfg.address}
        onSave={save}
        onTest={card.test}
        onRemove={card.remove}
      />
      <div className="mt-3 grid gap-3 sm:grid-cols-3">
        <Field label="Address">
          <Input
            value={cfg.address}
            onChange={(e) => setCfg((v) => ({ ...v, address: e.target.value }))}
            placeholder="https://vault.internal:8200"
            className="mt-1 h-8"
          />
        </Field>
        <Field label="Mount">
          <Input
            value={cfg.mount}
            onChange={(e) => setCfg((v) => ({ ...v, mount: e.target.value }))}
            placeholder="secret"
            className="mt-1 h-8"
          />
        </Field>
        <Field label="Auth method">
          <Select
            value={cfg.auth}
            onValueChange={(auth) => setCfg((v) => ({ ...v, auth }))}
          >
            <SelectTrigger className="mt-1 h-8 w-full text-xs" aria-label="vault auth method">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="token">Token</SelectItem>
              <SelectItem value="approle">AppRole</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        {cfg.auth === 'token' ? (
          <Field label="Token">
            <Input
              type="password"
              value={tokenPaste}
              onChange={(e) => setTokenPaste(e.target.value)}
              placeholder="paste to store/rotate"
              className="mt-1 h-8"
              autoComplete="off"
            />
          </Field>
        ) : (
          <>
            <Field label="Role ID">
              <Input
                value={cfg.role_id}
                onChange={(e) => setCfg((v) => ({ ...v, role_id: e.target.value }))}
                placeholder="role-id"
                className="mt-1 h-8"
              />
            </Field>
            <Field label="Secret ID">
              <Input
                type="password"
                value={secretIDPaste}
                onChange={(e) => setSecretIDPaste(e.target.value)}
                placeholder="paste to store/rotate"
                className="mt-1 h-8"
                autoComplete="off"
              />
            </Field>
          </>
        )}
      </div>
    </div>
  )
}

function ASMCard({ onError }: { onError: (msg: string) => void }) {
  const [cfg, setCfg] = useState({
    region: '',
    auth: 'chain',
    profile: '',
    access_key_id: '',
    secret_key_ref: '',
  })
  const [secretKeyPaste, setSecretKeyPaste] = useState('')
  const card = useBackendCard(
    'asm',
    (c) => setCfg((v) => ({ ...v, ...c, auth: c.auth || 'chain' })),
    onError,
  )

  const save = () =>
    card.run(async () => {
      await putSecretBackendConfig('asm', cfg)
      if (cfg.auth === 'keys' && secretKeyPaste) {
        await setSecret(cfg.secret_key_ref || 'AWS_SECRET_ACCESS_KEY', secretKeyPaste)
        setSecretKeyPaste('')
      }
      card.setConfigured(true)
      return 'saved'
    })

  return (
    <div className="rounded-xl border border-border p-4">
      <BackendCardHeader
        title="AWS Secrets Manager"
        subtitle="Test requires secretsmanager:ListSecrets; resolving keys only needs GetSecretValue."
        configured={card.configured}
        status={card.status}
        busy={card.busy}
        canSave
        onSave={save}
        onTest={card.test}
        onRemove={card.remove}
      />
      <div className="mt-3 grid gap-3 sm:grid-cols-3">
        <Field label="Region">
          <Input
            value={cfg.region}
            onChange={(e) => setCfg((v) => ({ ...v, region: e.target.value }))}
            placeholder="empty = chain default"
            className="mt-1 h-8"
          />
        </Field>
        <Field label="Auth method">
          <Select value={cfg.auth} onValueChange={(auth) => setCfg((v) => ({ ...v, auth }))}>
            <SelectTrigger className="mt-1 h-8 w-full text-xs" aria-label="aws auth method">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="chain">Credential chain</SelectItem>
              <SelectItem value="profile">Named profile</SelectItem>
              <SelectItem value="keys">Access keys</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        {cfg.auth === 'chain' && (
          <p className="self-end pb-1.5 text-xs text-muted-foreground">
            Uses the chain mounted into the gateway — same as Bedrock, nothing stored.
          </p>
        )}
        {cfg.auth === 'profile' && (
          <Field label="Profile">
            <Input
              value={cfg.profile}
              onChange={(e) => setCfg((v) => ({ ...v, profile: e.target.value }))}
              placeholder="profile name from ~/.aws"
              className="mt-1 h-8"
            />
          </Field>
        )}
        {cfg.auth === 'keys' && (
          <>
            <Field label="Access key ID">
              <Input
                value={cfg.access_key_id}
                onChange={(e) => setCfg((v) => ({ ...v, access_key_id: e.target.value }))}
                placeholder="AKIA…"
                className="mt-1 h-8"
              />
            </Field>
            <Field label="Secret access key">
              <Input
                type="password"
                value={secretKeyPaste}
                onChange={(e) => setSecretKeyPaste(e.target.value)}
                placeholder="paste to store/rotate"
                className="mt-1 h-8"
                autoComplete="off"
              />
            </Field>
          </>
        )}
      </div>
    </div>
  )
}

function SecretsTab() {
  const [error, setError] = useState<string | null>(null)
  return (
    <div className="mt-6 space-y-4">
      <p className="text-sm text-muted-foreground">
        Where provider keys live. By default each key is encrypted with the master key and kept
        in Timothy&apos;s own database — nothing to configure. Connect Vault or AWS Secrets
        Manager to resolve keys from existing secret infrastructure instead; pick the source
        per key on its provider card. OAuth-based connections arrive with connectors.
      </p>
      {error && (
        <div className="rounded-xl border border-red-500/30 bg-red-500/5 p-3 text-sm text-red-600 dark:text-red-400">
          {error}
        </div>
      )}
      <VaultCard onError={setError} />
      <ASMCard onError={setError} />
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
        <Select value={driver} onValueChange={setDriver}>
          <SelectTrigger className="h-9 w-full" aria-label="Driver">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="openaicompat">openaicompat</SelectItem>
            <SelectItem value="anthropic">anthropic</SelectItem>
            <SelectItem value="bedrock">bedrock</SelectItem>
            <SelectItem value="cli" disabled>
              cli — driver available in a later phase
            </SelectItem>
          </SelectContent>
        </Select>
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
      <Select
        value={providerID}
        onValueChange={(id) => {
          setProviderID(id)
          const p = providers.find((x) => x.id === id)
          if (p?.default_model) setModel(p.default_model)
        }}
      >
        <SelectTrigger className="h-8 w-40" aria-label="Provider">
          <SelectValue placeholder="provider…" />
        </SelectTrigger>
        <SelectContent>
          {providers.map((p) => (
            <SelectItem key={p.id} value={p.id}>
              {p.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
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
