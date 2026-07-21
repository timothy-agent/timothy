import { Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useRef, useState } from 'react'
import {
  deleteSecretBackendConfig,
  getSecretBackendConfig,
  putSecretBackendConfig,
  setSecret,
  testSecretBackend,
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
import { ErrorBanner, Field } from './shared'
import { errText } from './util'

export function SecretsTab() {
  const [error, setError] = useState<string | null>(null)
  return (
    <div className="mt-6 space-y-4">
      <p className="text-sm text-muted-foreground">
        Where provider keys live. By default each key is encrypted with the master key and kept
        in Timothy&apos;s own database — nothing to configure. Connect Vault or AWS Secrets
        Manager to resolve keys from existing secret infrastructure instead; pick the source
        per key on its provider card. OAuth-based connections arrive with connectors.
      </p>
      <ErrorBanner message={error} />
      <VaultCard onError={setError} />
      <ASMCard onError={setError} />
    </div>
  )
}

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
