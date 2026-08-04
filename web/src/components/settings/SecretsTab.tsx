import { Delete02Icon, LockIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon, type IconSvgElement } from '@hugeicons/react'
import { useCallback, useEffect, useRef, useState } from 'react'
import {
  deleteSecretBackendConfig,
  getSecretBackendConfig,
  listSecretBackends,
  putSecretBackendConfig,
  secretStatus,
  setDefaultSecretBackend,
  setSecretStorage,
  testSecretBackend,
  type SecretBackendStatus,
} from '../../api/client'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../ui/select'
import { awsRegions } from './presets'
import { ErrorBanner, Field } from './shared'
import { errText } from './util'

// Brand colors for the official marks in ProviderLogo's sprite
// (plogo-aws, plogo-vault), matching their real product colors.
const AWS_BRAND_COLOR = '#232F3E'
const VAULT_BRAND_COLOR = '#000000'

const ASM_REGION_CHAIN_DEFAULT = '__chain_default__'

export function SecretsTab() {
  const [error, setError] = useState<string | null>(null)
  const [backends, setBackends] = useState<SecretBackendStatus[]>([])

  const refresh = useCallback(() => {
    listSecretBackends().then(setBackends, (err: unknown) => setError(errText(err)))
  }, [])
  useEffect(refresh, [refresh])

  const state = (b: string) => backends.find((x) => x.backend === b)
  const makeDefault = (b: string) => {
    setDefaultSecretBackend(b).then(refresh, (err: unknown) => setError(errText(err)))
  }

  // Cards render default-first, so the backend everything else routes
  // through is always the top card regardless of setup order.
  const cards = [
    {
      key: 'db',
      isDefault: state('db')?.default ?? true,
      render: () => (
        <StorageCard isDefault={state('db')?.default ?? true} onMakeDefault={() => makeDefault('db')} />
      ),
    },
    {
      key: 'vault',
      isDefault: state('vault')?.default ?? false,
      render: () => (
        <VaultCard
          isDefault={state('vault')?.default ?? false}
          onMakeDefault={() => makeDefault('vault')}
          onBackendsChanged={refresh}
          onError={setError}
        />
      ),
    },
    {
      key: 'asm',
      isDefault: state('asm')?.default ?? false,
      render: () => (
        <ASMCard
          isDefault={state('asm')?.default ?? false}
          onMakeDefault={() => makeDefault('asm')}
          onBackendsChanged={refresh}
          onError={setError}
        />
      ),
    },
  ].sort((a, b) => Number(b.isDefault) - Number(a.isDefault))

  return (
    <div className="mt-6 space-y-4">
      <p className="text-sm text-muted-foreground">
        Where credentials live. Exactly one backend is the default: every key or token entered
        anywhere (providers, connectors) is written there by Timothy. Timothy storage keeps
        values encrypted in its own database; making Vault or AWS Secrets Manager the default
        means Timothy needs write access there, every key entered in the UI is written into it
        under a timothy/ prefix.
      </p>
      <ErrorBanner message={error} />
      {cards.map((c) => (
        <div key={c.key}>{c.render()}</div>
      ))}
    </div>
  )
}

// BackendIcon tiles a backend's icon consistently across cards. A
// generic Hugeicon renders muted (Timothy storage); a `logo` name
// renders the official mark from ProviderLogo's sprite on its
// brand-color tile, same treatment as a configured provider.
function BackendIcon({
  icon,
  logo,
  brandColor,
  className = 'size-9',
}: {
  icon?: IconSvgElement
  logo?: string
  brandColor?: string
  className?: string
}) {
  if (logo) {
    return (
      <span
        className={`${className} grid shrink-0 place-items-center rounded-lg text-white`}
        style={{ backgroundColor: brandColor }}
        aria-hidden="true"
      >
        <svg className="size-[60%] fill-current">
          <use href={`#plogo-${logo}`} />
        </svg>
      </span>
    )
  }
  return (
    <span
      className={`flex shrink-0 items-center justify-center rounded-lg bg-muted text-muted-foreground ${className}`}
    >
      {icon && <HugeiconsIcon icon={icon} className="size-4.5" />}
    </span>
  )
}

// DefaultControl is the per-card default marker: a badge when the card
// is the default, a button to claim it otherwise.
function DefaultControl({
  isDefault,
  disabled,
  onMakeDefault,
}: {
  isDefault: boolean
  disabled?: boolean
  onMakeDefault: () => void
}) {
  if (isDefault) {
    return (
      <span className="rounded bg-sky-500/15 px-1.5 py-px text-[10px] font-medium uppercase text-sky-600 dark:text-sky-400">
        default
      </span>
    )
  }
  return (
    <Button size="sm" variant="outline" disabled={disabled} onClick={onMakeDefault}>
      Make default
    </Button>
  )
}

// StorageCard is the built-in backend: nothing to configure, always
// available, keys envelope-encrypted with the master key.
function StorageCard({
  isDefault,
  onMakeDefault,
}: {
  isDefault: boolean
  onMakeDefault: () => void
}) {
  return (
    <div className="rounded-xl border border-border p-4">
      <div className="flex flex-wrap items-center gap-3">
        <BackendIcon icon={LockIcon} />
        <span className="font-medium">Timothy storage</span>
        <span className="rounded bg-emerald-500/15 px-1.5 py-px text-[10px] font-medium uppercase text-emerald-600 dark:text-emerald-400">
          built-in
        </span>
        <div className="ml-auto flex items-center gap-3">
          <DefaultControl isDefault={isDefault} onMakeDefault={onMakeDefault} />
        </div>
      </div>
      <p className="mt-1 text-xs text-muted-foreground">
        Keys are envelope-encrypted with the master key and kept in Timothy&apos;s own database.
        Nothing to configure.
      </p>
    </div>
  )
}

// useBackendCard holds the shared load/save/test/remove machinery for
// one external secret backend's card.
function useBackendCard(
  backend: 'vault' | 'asm',
  onLoaded: (cfg: Record<string, string>) => void,
  onError: (msg: string) => void,
  onChanged: () => void,
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
      .catch((err: unknown) => onError(errText(err)))
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
      // Removing the default backend hands the flag back to storage;
      // the parent re-fetches so badges follow.
      onChanged()
      return 'removed'
    })

  return { configured, setConfigured, busy, status, run, test, remove }
}

function BackendCardHeader({
  icon,
  logo,
  brandColor,
  title,
  subtitle,
  configured,
  status,
  busy,
  canSave,
  isDefault,
  onMakeDefault,
  onSave,
  onTest,
  onRemove,
}: {
  icon?: IconSvgElement
  logo?: string
  brandColor?: string
  title: string
  subtitle: string
  configured: boolean
  status: string
  busy: boolean
  canSave: boolean
  isDefault: boolean
  onMakeDefault: () => void
  onSave: () => void
  onTest: () => void
  onRemove: () => void
}) {
  return (
    <>
      <div className="flex flex-wrap items-center gap-3">
        <BackendIcon icon={icon} logo={logo} brandColor={brandColor} />
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
          <DefaultControl
            isDefault={isDefault}
            disabled={busy || !configured}
            onMakeDefault={onMakeDefault}
          />
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

type BackendCardProps = {
  isDefault: boolean
  onMakeDefault: () => void
  onBackendsChanged: () => void
  onError: (msg: string) => void
}

// useCredentialStatus reports whether refName already holds a value
// in the (always db-backed) secret store, for the "stored · encrypted"
// badge next to a backend's own bootstrap credential (Vault token, ASM
// secret key) — mirrors ProviderEdit's credential badge.
function useCredentialStatus(refName: string, dep: unknown): boolean {
  const [configured, setConfigured] = useState(false)
  useEffect(() => {
    if (!refName) {
      setConfigured(false)
      return
    }
    secretStatus(refName).then(
      (s) => setConfigured(s.configured),
      () => setConfigured(false),
    )
    // dep re-runs the check after a save/remove changes the ref's status.
  }, [refName, dep])
  return configured
}

function StoredBadge({ configured }: { configured: boolean }) {
  if (!configured) return null
  return (
    <span className="shrink-0 rounded bg-good-soft px-1.5 py-0.5 text-[10px] font-semibold uppercase text-good">
      stored · encrypted
    </span>
  )
}

function VaultCard({ isDefault, onMakeDefault, onBackendsChanged, onError }: BackendCardProps) {
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
    onBackendsChanged,
  )
  const tokenStored = useCredentialStatus(cfg.token_ref || 'VAULT_TOKEN', card.status)
  const secretIDStored = useCredentialStatus(cfg.secret_id_ref || 'VAULT_SECRET_ID', card.status)

  const save = () =>
    card.run(async () => {
      await putSecretBackendConfig('vault', cfg)
      if (cfg.auth === 'token' && tokenPaste) {
        await setSecretStorage(cfg.token_ref || 'VAULT_TOKEN', tokenPaste)
        setTokenPaste('')
      }
      if (cfg.auth === 'approle' && secretIDPaste) {
        await setSecretStorage(cfg.secret_id_ref || 'VAULT_SECRET_ID', secretIDPaste)
        setSecretIDPaste('')
      }
      card.setConfigured(true)
      onBackendsChanged()
      return 'saved'
    })

  return (
    <div className="rounded-xl border border-border p-4">
      <BackendCardHeader
        logo="vault"
        brandColor={VAULT_BRAND_COLOR}
        title="HashiCorp Vault"
        subtitle="KV v2 mount. Timothy needs write access: every key entered in the UI is written here as the default."
        configured={card.configured}
        status={card.status}
        busy={card.busy}
        canSave={!!cfg.address}
        isDefault={isDefault}
        onMakeDefault={onMakeDefault}
        onSave={save}
        onTest={card.test}
        onRemove={card.remove}
      />
      <div className="mt-3 grid gap-3 sm:grid-cols-2">
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
          <Field label="Token" className="sm:col-span-2">
            <div className="mt-1 flex items-center gap-2">
              <Input
                type="password"
                value={tokenPaste}
                onChange={(e) => setTokenPaste(e.target.value)}
                placeholder="paste to store/rotate"
                className="h-8"
                autoComplete="off"
              />
              <StoredBadge configured={tokenStored} />
            </div>
          </Field>
        ) : (
          <>
            <Field label="Role ID" className="sm:col-span-2">
              <Input
                value={cfg.role_id}
                onChange={(e) => setCfg((v) => ({ ...v, role_id: e.target.value }))}
                placeholder="role-id"
                className="mt-1 h-8"
              />
            </Field>
            <Field label="Secret ID" className="sm:col-span-2">
              <div className="mt-1 flex items-center gap-2">
                <Input
                  type="password"
                  value={secretIDPaste}
                  onChange={(e) => setSecretIDPaste(e.target.value)}
                  placeholder="paste to store/rotate"
                  className="h-8"
                  autoComplete="off"
                />
                <StoredBadge configured={secretIDStored} />
              </div>
            </Field>
          </>
        )}
      </div>
    </div>
  )
}

function ASMCard({ isDefault, onMakeDefault, onBackendsChanged, onError }: BackendCardProps) {
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
    onBackendsChanged,
  )
  const secretKeyStored = useCredentialStatus(cfg.secret_key_ref || 'AWS_SECRET_ACCESS_KEY', card.status)

  const save = () =>
    card.run(async () => {
      await putSecretBackendConfig('asm', cfg)
      if (cfg.auth === 'keys' && secretKeyPaste) {
        await setSecretStorage(cfg.secret_key_ref || 'AWS_SECRET_ACCESS_KEY', secretKeyPaste)
        setSecretKeyPaste('')
      }
      card.setConfigured(true)
      onBackendsChanged()
      return 'saved'
    })

  return (
    <div className="rounded-xl border border-border p-4">
      <BackendCardHeader
        logo="aws"
        brandColor={AWS_BRAND_COLOR}
        title="AWS Secrets Manager"
        subtitle="Timothy needs write access (CreateSecret/PutSecretValue) to store keys here as the default."
        configured={card.configured}
        status={card.status}
        busy={card.busy}
        canSave
        isDefault={isDefault}
        onMakeDefault={onMakeDefault}
        onSave={save}
        onTest={card.test}
        onRemove={card.remove}
      />
      <div className="mt-3 grid gap-3 sm:grid-cols-2">
        <Field label="Region">
          <Select
            value={cfg.region || ASM_REGION_CHAIN_DEFAULT}
            onValueChange={(v) =>
              setCfg((c) => ({ ...c, region: v === ASM_REGION_CHAIN_DEFAULT ? '' : v }))
            }
          >
            <SelectTrigger className="mt-1 h-8 w-full text-xs" aria-label="aws region">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ASM_REGION_CHAIN_DEFAULT}>Chain default</SelectItem>
              {awsRegions.map((r) => (
                <SelectItem key={r.value} value={r.value}>
                  {r.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
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
        {cfg.auth === 'profile' && (
          <Field label="Profile" className="sm:col-span-2">
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
            <Field label="Access key ID" className="sm:col-span-2">
              <Input
                value={cfg.access_key_id}
                onChange={(e) => setCfg((v) => ({ ...v, access_key_id: e.target.value }))}
                placeholder="AKIA…"
                className="mt-1 h-8"
              />
            </Field>
            <Field label="Secret access key" className="sm:col-span-2">
              <div className="mt-1 flex items-center gap-2">
                <Input
                  type="password"
                  value={secretKeyPaste}
                  onChange={(e) => setSecretKeyPaste(e.target.value)}
                  placeholder="paste to store/rotate"
                  className="h-8"
                  autoComplete="off"
                />
                <StoredBadge configured={secretKeyStored} />
              </div>
            </Field>
          </>
        )}
      </div>
    </div>
  )
}
