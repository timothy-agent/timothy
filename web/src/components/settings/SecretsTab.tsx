import { Eye, EyeOff, Lock } from 'lucide-react'
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
import { Badge } from '../ui/badge'
import { Input } from '../ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../ui/select'
import { awsRegions } from './presets'
import { Alert, AlertDescription } from '../ui/alert'
import { BrandTile } from '../timothy/brand-tile'
import { Field } from '../timothy/field'
import { IconButton } from '../timothy/icon-button'
import { Panel } from '../timothy/panel'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { TestStatus } from './TestStatus'
import { TooltipProvider } from '../ui/tooltip'
import { settingsArea } from './settingsAreas'
import { errText } from './util'

const area = settingsArea('secrets')

// Brand colors for the official marks, matching their real product colors.
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
          configured={state('vault')?.configured ?? false}
          onMakeDefault={() => makeDefault('vault')}
          onBackendsChanged={refresh}
        />
      ),
    },
    {
      key: 'asm',
      isDefault: state('asm')?.default ?? false,
      render: () => (
        <ASMCard
          isDefault={state('asm')?.default ?? false}
          configured={state('asm')?.configured ?? false}
          onMakeDefault={() => makeDefault('asm')}
          onBackendsChanged={refresh}
        />
      ),
    },
  ].sort((a, b) => Number(b.isDefault) - Number(a.isDefault))

  return (
    // SecretsTab renders its own TooltipProvider: pages/Settings.test.tsx
    // exercises this tab without mounting App's provider, and the reveal
    // toggle on Vault/AWS token inputs needs one.
    <TooltipProvider delayDuration={300}>
      <PageShell>
        <PageHeader
          title={area.label}
          description={area.description}
          breadcrumbs={[{ label: 'Settings', href: '/settings' }, { label: area.label }]}
        />
        <div className="space-y-4">
          <p className="text-sm text-muted-foreground">
            Where credentials live. Exactly one backend is the default: every key or token entered
            anywhere (providers, connectors) is written there by Timothy. Timothy storage keeps
            values encrypted in its own database; making Vault or AWS Secrets Manager the default
            means Timothy needs write access there, every key entered in the UI is written into it
            under a timothy/ prefix.
          </p>
          {error && (
            <Alert tone="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          {cards.map((c) => (
            <div key={c.key}>{c.render()}</div>
          ))}
        </div>
      </PageShell>
    </TooltipProvider>
  )
}

// DefaultControl is the per-card default marker: a badge when the card
// is the default, a button to claim it otherwise. Claiming the default
// is an atomic preference (10.7): it commits immediately.
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
      <Badge variant="info" className="uppercase tracking-wide">
        default
      </Badge>
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
    <Panel
      title="Timothy storage"
      actions={<DefaultControl isDefault={isDefault} onMakeDefault={onMakeDefault} />}
    >
      <div className="flex items-start gap-3">
        <BrandTile icon={<Lock className="size-4.5" aria-hidden />} label="Timothy storage" />
        <div className="space-y-1">
          <Badge variant="good" className="uppercase tracking-wide">
            built-in
          </Badge>
          <p className="text-sm text-muted-foreground">
            Keys are envelope-encrypted with the master key and kept in Timothy&apos;s own database.
            Nothing to configure.
          </p>
        </div>
      </div>
    </Panel>
  )
}

// useCredentialStatus reports whether refName already holds a value
// in the (always db-backed) secret store, for the "stored" badge next
// to a backend's own bootstrap credential (Vault token, ASM secret
// key), mirroring ProviderEdit's credential badge.
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
    <Badge variant="good" size="sm" className="uppercase tracking-wide">
      stored · encrypted
    </Badge>
  )
}

// RevealInput is a password input for a secret paste, with a reveal
// toggle (contract 10.6). Value never round-trips from the server;
// the field only ever holds what the user just typed.
function RevealInput({
  id,
  value,
  onChange,
  placeholder,
}: {
  id: string
  value: string
  onChange: (v: string) => void
  placeholder: string
}) {
  const [revealed, setRevealed] = useState(false)
  return (
    <div className="flex items-center gap-2">
      <Input
        id={id}
        type={revealed ? 'text' : 'password'}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        autoComplete="off"
      />
      <IconButton
        label={revealed ? 'Hide token' : 'Show token'}
        icon={revealed ? EyeOff : Eye}
        variant="outline"
        onClick={() => setRevealed((v) => !v)}
      />
    </div>
  )
}

// useBackendCard holds the shared load/save/test/remove machinery for
// one external secret backend's card. Test status is a typed object
// (state, message), never a string prefix check, so TestStatus renders
// the same Alert shape every other test/probe surface uses.
function useBackendCard(
  backend: 'vault' | 'asm',
  onLoaded: (cfg: Record<string, string>) => void,
  onChanged: () => void,
) {
  const [configured, setConfigured] = useState(false)
  const [busy, setBusy] = useState(false)
  const [testState, setTestState] = useState<{ state: 'idle' | 'testing' | 'ok' | 'failed'; message?: string }>({
    state: 'idle',
  })

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

  const test = async () => {
    setBusy(true)
    setTestState({ state: 'testing' })
    try {
      const res = await testSecretBackend(backend)
      setTestState(
        res.ok
          ? { state: 'ok', message: 'connection OK' }
          : { state: 'failed', message: res.error ?? 'Connection failed.' },
      )
    } catch (err) {
      setTestState({ state: 'failed', message: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    setBusy(true)
    try {
      await deleteSecretBackendConfig(backend)
      setConfigured(false)
      setTestState({ state: 'ok', message: 'removed' })
      // Removing the default backend hands the flag back to storage;
      // the parent re-fetches so badges follow.
      onChanged()
    } catch (err) {
      setTestState({ state: 'failed', message: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  return { configured, setConfigured, busy, setBusy, testState, setTestState, test, remove }
}

function ConfiguredBadge({ configured }: { configured: boolean }) {
  return (
    <Badge variant={configured ? 'good' : 'neutral'} className="uppercase tracking-wide">
      {configured ? 'configured' : 'not configured'}
    </Badge>
  )
}

function VaultCard({
  isDefault,
  configured: backendConfigured,
  onMakeDefault,
  onBackendsChanged,
}: {
  isDefault: boolean
  configured: boolean
  onMakeDefault: () => void
  onBackendsChanged: () => void
}) {
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
    onBackendsChanged,
  )
  const tokenStored = useCredentialStatus(cfg.token_ref || 'VAULT_TOKEN', card.testState)
  const secretIDStored = useCredentialStatus(cfg.secret_id_ref || 'VAULT_SECRET_ID', card.testState)

  const canSave = !!cfg.address

  const save = async () => {
    card.setBusy(true)
    try {
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
      card.setTestState({ state: 'ok', message: 'saved' })
      onBackendsChanged()
    } catch (err) {
      card.setTestState({ state: 'failed', message: errText(err) })
    } finally {
      card.setBusy(false)
    }
  }

  return (
    <Panel
      title="HashiCorp Vault"
      description="KV v2 mount. Timothy needs write access: every key entered in the UI is written here as the default."
      actions={
        <div className="flex items-center gap-2">
          <ConfiguredBadge configured={card.configured} />
          <DefaultControl
            isDefault={isDefault}
            disabled={card.busy || !card.configured}
            onMakeDefault={onMakeDefault}
          />
        </div>
      }
    >
      <div className="space-y-4">
        <BrandTile spriteId="plogo-vault" color={VAULT_BRAND_COLOR} label="HashiCorp Vault" />
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Address">
            <Input
              value={cfg.address}
              onChange={(e) => setCfg((v) => ({ ...v, address: e.target.value }))}
              placeholder="https://vault.internal:8200"
            />
          </Field>
          <Field label="Mount">
            <Input
              value={cfg.mount}
              onChange={(e) => setCfg((v) => ({ ...v, mount: e.target.value }))}
              placeholder="secret"
            />
          </Field>
          <Field label="Auth method">
            {(props) => (
              <Select value={cfg.auth} onValueChange={(auth) => setCfg((v) => ({ ...v, auth }))}>
                <SelectTrigger id={props.id} className="w-full" aria-label="vault auth method">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="token">Token</SelectItem>
                  <SelectItem value="approle">AppRole</SelectItem>
                </SelectContent>
              </Select>
            )}
          </Field>
          {cfg.auth === 'token' ? (
            <div className="sm:col-span-2">
              <Field label="Token" description="Write-only. A stored token is never shown back.">
                {(props) => (
                  <div className="flex items-center gap-2">
                    <div className="flex-1">
                      <RevealInput
                        id={props.id}
                        value={tokenPaste}
                        onChange={setTokenPaste}
                        placeholder="paste to store/rotate"
                      />
                    </div>
                    <StoredBadge configured={tokenStored} />
                  </div>
                )}
              </Field>
            </div>
          ) : (
            <>
              <div className="sm:col-span-2">
                <Field label="Role ID">
                  <Input
                    value={cfg.role_id}
                    onChange={(e) => setCfg((v) => ({ ...v, role_id: e.target.value }))}
                    placeholder="role-id"
                  />
                </Field>
              </div>
              <div className="sm:col-span-2">
                <Field label="Secret ID" description="Write-only. A stored secret ID is never shown back.">
                  {(props) => (
                    <div className="flex items-center gap-2">
                      <div className="flex-1">
                        <RevealInput
                          id={props.id}
                          value={secretIDPaste}
                          onChange={setSecretIDPaste}
                          placeholder="paste to store/rotate"
                        />
                      </div>
                      <StoredBadge configured={secretIDStored} />
                    </div>
                  )}
                </Field>
              </div>
            </>
          )}
        </div>
        <TestStatus state={card.testState.state} message={card.testState.message} />
        <div className="flex items-center gap-2">
          <Button variant="outline" disabled={card.busy || !card.configured} onClick={() => void card.test()}>
            Test
          </Button>
          <Button disabled={card.busy || !canSave} onClick={() => void save()}>
            Save
          </Button>
          {backendConfigured && (
            <Button variant="outline" aria-label="Remove HashiCorp Vault" disabled={card.busy} onClick={() => void card.remove()}>
              Remove
            </Button>
          )}
        </div>
      </div>
    </Panel>
  )
}

function ASMCard({
  isDefault,
  configured: backendConfigured,
  onMakeDefault,
  onBackendsChanged,
}: {
  isDefault: boolean
  configured: boolean
  onMakeDefault: () => void
  onBackendsChanged: () => void
}) {
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
    onBackendsChanged,
  )
  const secretKeyStored = useCredentialStatus(cfg.secret_key_ref || 'AWS_SECRET_ACCESS_KEY', card.testState)

  const save = async () => {
    card.setBusy(true)
    try {
      await putSecretBackendConfig('asm', cfg)
      if (cfg.auth === 'keys' && secretKeyPaste) {
        await setSecretStorage(cfg.secret_key_ref || 'AWS_SECRET_ACCESS_KEY', secretKeyPaste)
        setSecretKeyPaste('')
      }
      card.setConfigured(true)
      card.setTestState({ state: 'ok', message: 'saved' })
      onBackendsChanged()
    } catch (err) {
      card.setTestState({ state: 'failed', message: errText(err) })
    } finally {
      card.setBusy(false)
    }
  }

  return (
    <Panel
      title="AWS Secrets Manager"
      description="Timothy needs write access (CreateSecret/PutSecretValue) to store keys here as the default."
      actions={
        <div className="flex items-center gap-2">
          <ConfiguredBadge configured={card.configured} />
          <DefaultControl
            isDefault={isDefault}
            disabled={card.busy || !card.configured}
            onMakeDefault={onMakeDefault}
          />
        </div>
      }
    >
      <div className="space-y-4">
        <BrandTile spriteId="plogo-aws" color={AWS_BRAND_COLOR} label="AWS Secrets Manager" />
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Region">
            {(props) => (
              <Select
                value={cfg.region || ASM_REGION_CHAIN_DEFAULT}
                onValueChange={(v) =>
                  setCfg((c) => ({ ...c, region: v === ASM_REGION_CHAIN_DEFAULT ? '' : v }))
                }
              >
                <SelectTrigger id={props.id} className="w-full" aria-label="aws region">
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
            )}
          </Field>
          <Field label="Auth method">
            {(props) => (
              <Select value={cfg.auth} onValueChange={(auth) => setCfg((v) => ({ ...v, auth }))}>
                <SelectTrigger id={props.id} className="w-full" aria-label="aws auth method">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="chain">Credential chain</SelectItem>
                  <SelectItem value="profile">Named profile</SelectItem>
                  <SelectItem value="keys">Access keys</SelectItem>
                </SelectContent>
              </Select>
            )}
          </Field>
          {cfg.auth === 'profile' && (
            <div className="sm:col-span-2">
              <Field label="Profile">
                <Input
                  value={cfg.profile}
                  onChange={(e) => setCfg((v) => ({ ...v, profile: e.target.value }))}
                  placeholder="profile name from ~/.aws"
                />
              </Field>
            </div>
          )}
          {cfg.auth === 'keys' && (
            <>
              <div className="sm:col-span-2">
                <Field label="Access key ID">
                  <Input
                    value={cfg.access_key_id}
                    onChange={(e) => setCfg((v) => ({ ...v, access_key_id: e.target.value }))}
                    placeholder="AKIA..."
                  />
                </Field>
              </div>
              <div className="sm:col-span-2">
                <Field label="Secret access key" description="Write-only. A stored key is never shown back.">
                  {(props) => (
                    <div className="flex items-center gap-2">
                      <div className="flex-1">
                        <RevealInput
                          id={props.id}
                          value={secretKeyPaste}
                          onChange={setSecretKeyPaste}
                          placeholder="paste to store/rotate"
                        />
                      </div>
                      <StoredBadge configured={secretKeyStored} />
                    </div>
                  )}
                </Field>
              </div>
            </>
          )}
        </div>
        <TestStatus state={card.testState.state} message={card.testState.message} />
        <div className="flex items-center gap-2">
          <Button variant="outline" disabled={card.busy || !card.configured} onClick={() => void card.test()}>
            Test
          </Button>
          <Button disabled={card.busy} onClick={() => void save()}>
            Save
          </Button>
          {backendConfigured && (
            <Button variant="outline" aria-label="Remove AWS Secrets Manager" disabled={card.busy} onClick={() => void card.remove()}>
              Remove
            </Button>
          )}
        </div>
      </div>
    </Panel>
  )
}
