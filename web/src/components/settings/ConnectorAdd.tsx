import { ArrowLeft01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useState } from 'react'
import { Link, Navigate, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import {
  connectorOAuthStart,
  createConnector,
  patchConnector,
  setSecret,
  testConnector,
} from '../../api/client'
import type { GitHubIdentity } from '../../api/types'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { ConnectorLogo } from './ConnectorLogo'
import { connectorPresets } from './connectorPresets'
import { CredentialModeToggle, ExistingCredentialSelect, type CredentialMode } from './CredentialRefPicker'
import { Field } from './shared'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'
import { connectedAs, errText, isTimothyAuthError, secretDestination } from './util'

// slugify turns a display name into a connector name (tool-name
// prefix): lowercase slug, the backend rejects anything else.
function slugify(v: string): string {
  return v
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

// ConnectorAdd is preset-aware and its own page: MCP and github presets
// are created, tested, and enabled in one go with Add gated on a
// passing test (same contract as adding a provider); Google presets
// take the OAuth client and hand off to Google's consent screen
// instead — there is no unsaved test to run before that redirect.
export function ConnectorAdd() {
  const { presetId } = useParams()
  const navigate = useNavigate()
  const preset = connectorPresets.find((p) => p.id === presetId)
  const defaultBackend = useDefaultSecretBackend()

  const [name, setName] = useState('')
  const [endpoint, setEndpoint] = useState('')
  const [token, setToken] = useState('')
  const [clientID, setClientID] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [busy, setBusy] = useState(false)
  const [test, setTest] = useState<{ ok: boolean; error?: string; identity?: GitHubIdentity } | null>(
    null,
  )
  // The connector row is created (disabled) as part of testing an MCP
  // or github preset — there's no unsaved-config validate endpoint like
  // providers have. createdID tracks that row so a passing test's
  // Add just enables it rather than creating a second one.
  const [createdID, setCreatedID] = useState<string | null>(null)
  // tokenCredMode/clientSecretCredMode: "new" pastes+stores a fresh
  // secret (current behavior); "existing" reuses a stored ref instead
  // — e.g. the same GitHub PAT already used by another connector, or
  // the same Google OAuth client credentials across gmail/calendar.
  const [tokenCredMode, setTokenCredMode] = useState<CredentialMode>('new')
  const [existingTokenRef, setExistingTokenRef] = useState('')
  const [clientSecretCredMode, setClientSecretCredMode] = useState<CredentialMode>('new')
  const [existingClientSecretRef, setExistingClientSecretRef] = useState('')

  useEffect(() => {
    if (!preset) return
    setName(slugify(preset.id === 'custom-mcp' ? '' : preset.name))
    setEndpoint(preset.endpoint ?? '')
    setToken('')
    setClientID('')
    setClientSecret('')
    setBusy(false)
    setTest(null)
    setCreatedID(null)
    setTokenCredMode('new')
    setExistingTokenRef('')
    setClientSecretCredMode('new')
    setExistingClientSecretRef('')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [preset?.id])

  if (!preset) return <Navigate to="/settings/connectors" replace />
  const isGoogle = preset.kind === 'google'
  const isMicrosoft = preset.kind === 'microsoft'
  const isOAuth = isGoogle || isMicrosoft
  const isGitHub = preset.kind === 'github'
  const slug = slugify(name)
  const refBase = slug.toUpperCase().replace(/-/g, '_')
  const tested = test?.ok === true

  const invalidate = () => {
    setTest(null)
    setCreatedID(null)
  }

  const usingExistingToken = tokenCredMode === 'existing'

  const runTest = async () => {
    if (!slug) {
      toast.error('Name required', { description: 'Give this connector a unique name before testing.' })
      return
    }
    if (!isGitHub && !endpoint.trim()) {
      toast.error('Endpoint required', { description: 'An MCP endpoint is required to test this connector.' })
      return
    }
    if (isGitHub && !usingExistingToken && !token.trim()) {
      toast.error('Token required', { description: 'A personal access token is required to test this connector.' })
      return
    }
    if (usingExistingToken && !existingTokenRef) {
      toast.error('Credential required', { description: 'Choose an existing credential to reuse.' })
      return
    }
    setBusy(true)
    setTest(null)
    try {
      // Suffix without stuttering: a name already ending in the flavor
      // word ("github", "github-mcp") gets the bare _PAT/_TOKEN suffix.
      const tokenRef = usingExistingToken
        ? existingTokenRef
        : isGitHub
          ? refBase.endsWith('GITHUB')
            ? `${refBase}_PAT`
            : `${refBase}_GITHUB_PAT`
          : refBase.endsWith('_MCP')
            ? `${refBase}_TOKEN`
            : `${refBase}_MCP_TOKEN`
      if (!usingExistingToken && token) await setSecret(tokenRef, token.trim())
      const id = await createConnector(
        isGitHub
          ? { name: slug, kind: 'github', config: {}, credential_ref: tokenRef, enabled: false }
          : {
              name: slug,
              kind: 'mcp',
              config: { endpoint: endpoint.trim() },
              credential_ref: usingExistingToken || token ? tokenRef : '',
              enabled: false,
            },
      )
      setCreatedID(id)
      setTest(await testConnector(id))
    } catch (err) {
      if (isTimothyAuthError(err)) {
        setTest(null)
        return
      }
      setTest({ ok: false, error: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  const submit = async () => {
    if (!tested || !createdID) return
    setBusy(true)
    try {
      await patchConnector(createdID, { enabled: true })
      toast.success('Connector added', {
        description: isGitHub
          ? `${slug} is connected; its identity is ready for mission use.`
          : `${slug} is connected and tools are servable.`,
      })
      navigate('/settings/connectors')
    } catch (err) {
      toast.error('Could not enable connector', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  const usingExistingClientSecret = clientSecretCredMode === 'existing'

  const oauthProviderLabel = isMicrosoft ? 'Microsoft' : 'Google'
  const oauthSecretSuffix = isMicrosoft ? '_MICROSOFT_CLIENT_SECRET' : '_GOOGLE_CLIENT_SECRET'
  const oauthTokenSuffix = isMicrosoft ? '_MICROSOFT_OAUTH' : '_GOOGLE_OAUTH'

  const submitOAuth = async () => {
    setBusy(true)
    try {
      const secretRef = usingExistingClientSecret ? existingClientSecretRef : `${refBase}${oauthSecretSuffix}`
      if (!usingExistingClientSecret) await setSecret(secretRef, clientSecret)
      const id = await createConnector({
        name: slug,
        kind: isMicrosoft ? 'microsoft' : 'google',
        config: {
          client_id: clientID.trim(),
          client_secret_ref: secretRef,
          scopes: preset.scopes,
        },
        credential_ref: `${refBase}${oauthTokenSuffix}`,
        enabled: false,
      })
      window.location.assign(await connectorOAuthStart(id))
    } catch (err) {
      toast.error(`Could not connect ${oauthProviderLabel} account`, { description: errText(err) })
      setBusy(false)
    }
  }

  const canTest =
    slug !== '' &&
    (isGitHub ? (usingExistingToken ? existingTokenRef !== '' : token.trim() !== '') : endpoint.trim() !== '')
  const canSubmitOAuth =
    slug !== '' &&
    clientID.trim() !== '' &&
    (usingExistingClientSecret ? existingClientSecretRef !== '' : clientSecret !== '')

  return (
    <div className="mt-6 w-full space-y-6">
      <Link
        to="/settings/connectors"
        className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground transition hover:text-foreground"
      >
        <HugeiconsIcon icon={ArrowLeft01Icon} className="size-4" />
        Connectors
      </Link>

      <div className="flex items-center gap-4 border-b border-border pb-6">
        <ConnectorLogo preset={preset} className="size-12" />
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Add {preset.name}</h1>
          <p className="text-sm text-muted-foreground">kind: {preset.kind}</p>
        </div>
      </div>

      <div className="grid max-w-3xl gap-5">
        <Field label="Name" hint="lowercase slug, prefixes this connector's tool names">
          <Input
            value={name}
            onChange={(e) => {
              setName(e.target.value)
              invalidate()
            }}
            placeholder={preset.id === 'custom-mcp' ? 'my-server' : slugify(preset.name)}
            className="mt-1.5 h-10"
          />
        </Field>

        {isOAuth ? (
          <>
            <Field label="OAuth client ID">
              <Input
                value={clientID}
                onChange={(e) => setClientID(e.target.value)}
                placeholder={isMicrosoft ? 'application (client) ID' : '….apps.googleusercontent.com'}
                className="mt-1.5 h-10"
              />
            </Field>
            <div>
              <div className="mb-1.5 flex items-center justify-between">
                <span className="text-sm font-medium text-foreground">OAuth client secret</span>
                <CredentialModeToggle mode={clientSecretCredMode} onChange={setClientSecretCredMode} />
              </div>
              {clientSecretCredMode === 'existing' ? (
                <ExistingCredentialSelect
                  value={existingClientSecretRef}
                  onChange={setExistingClientSecretRef}
                />
              ) : (
                <Input
                  type="password"
                  value={clientSecret}
                  onChange={(e) => setClientSecret(e.target.value)}
                  placeholder={isMicrosoft ? 'client secret value' : 'GOCSPX-…'}
                  className="h-10"
                  autoComplete="off"
                />
              )}
            </div>
            <p className="text-sm text-muted-foreground">
              {isMicrosoft
                ? 'From an Azure AD app registration (multitenant, "Accounts in any organizational directory and personal Microsoft accounts"). Add'
                : 'From a Google Cloud OAuth client (Web application). Add'}{' '}
              <span className="font-mono">{window.location.origin}/v1/connectors/oauth/callback</span>{' '}
              to its {isMicrosoft ? 'redirect URIs (Web platform)' : 'authorized redirect URIs'}. Scopes:{' '}
              {preset.scopes?.map((s) => s.split('/').pop()).join(', ')}. Saving redirects you to{' '}
              {oauthProviderLabel} to consent.
            </p>
            <div className="flex gap-3 pt-2">
              <Button variant="outline" disabled={busy} onClick={() => navigate('/settings/connectors')}>
                Cancel
              </Button>
              <Button disabled={!canSubmitOAuth || busy} onClick={() => void submitOAuth()}>
                {busy ? 'Redirecting…' : `Save & connect ${oauthProviderLabel}`}
              </Button>
            </div>
          </>
        ) : (
          <>
            {!isGitHub && (
              <>
                <Field label="Endpoint">
                  <Input
                    value={endpoint}
                    onChange={(e) => {
                      setEndpoint(e.target.value)
                      invalidate()
                    }}
                    placeholder="https://…/mcp"
                    className="mt-1.5 h-10"
                  />
                </Field>
                {preset.endpointHint && (
                  <p className="-mt-3 text-sm text-muted-foreground">{preset.endpointHint}</p>
                )}
              </>
            )}
            <div>
              <div className="mb-1.5 flex items-center justify-between">
                <span className="text-sm font-medium text-foreground">
                  {isGitHub ? 'Personal access token' : preset.id === 'custom-mcp' ? 'Bearer token (optional)' : 'Bearer token'}
                </span>
                <CredentialModeToggle
                  mode={tokenCredMode}
                  onChange={(m) => {
                    setTokenCredMode(m)
                    invalidate()
                  }}
                />
              </div>
              {tokenCredMode === 'existing' ? (
                <ExistingCredentialSelect
                  value={existingTokenRef}
                  onChange={(v) => {
                    setExistingTokenRef(v)
                    invalidate()
                  }}
                />
              ) : (
                <>
                  <Input
                    type="password"
                    value={token}
                    onChange={(e) => {
                      setToken(e.target.value)
                      invalidate()
                    }}
                    placeholder={preset.tokenPlaceholder ?? 'token'}
                    className="h-10"
                    autoComplete="off"
                  />
                  <p className="mt-1.5 text-sm text-muted-foreground">
                    {preset.tokenHint}
                    {preset.tokenURL && (
                      <>
                        {' '}
                        <a
                          href={preset.tokenURL}
                          target="_blank"
                          rel="noreferrer"
                          className="font-medium text-primary underline underline-offset-2 hover:no-underline"
                        >
                          Create one on GitHub →
                        </a>
                      </>
                    )}
                    {!preset.tokenURL && (
                      <>
                        {' '}
                        {secretDestination(
                          defaultBackend,
                          isGitHub ? `${refBase}_GITHUB_PAT` : `${refBase}_MCP_TOKEN`,
                        )}
                      </>
                    )}
                  </p>
                </>
              )}
            </div>

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
                  ? 'Testing connection…'
                  : tested
                    ? test?.identity
                      ? `${connectedAs(test.identity)}, ${test.identity.scopes}.`
                      : 'Connection OK, tools are servable.'
                    : test && !test.ok
                      ? `Connection failed: ${test.error}. The connector was saved disabled, fix and retry.`
                      : 'Not tested yet, run a test before adding.'}
              </span>
              <Button size="sm" variant="test" disabled={busy || !canTest} onClick={() => void runTest()}>
                {busy ? 'Testing…' : 'Test connection'}
              </Button>
            </div>

            <div className="flex gap-3 pt-2">
              <Button variant="outline" disabled={busy} onClick={() => navigate('/settings/connectors')}>
                Cancel
              </Button>
              <Button disabled={!tested || busy} onClick={() => void submit()}>
                Add connector
              </Button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}
