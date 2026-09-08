import { useEffect, useState } from 'react'
import { Navigate, useNavigate, useParams } from 'react-router'
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
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { Field, FieldGroup, Form, FormActions } from '../timothy/field'
import { ConnectorLogo } from './ConnectorLogo'
import { connectorPresets } from './connectorPresets'
import { CredentialField, type CredentialMode } from './CredentialRefPicker'
import { settingsArea } from './settingsAreas'
import { TestStatus } from './TestStatus'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'
import { connectedAs } from './util'
import { errText, isTimothyAuthError } from '../../lib/errors'
import { slugify } from '../../lib/slugify'

const area = settingsArea('connectors')

// isValidPort reports whether an (optional) port field's text is a
// valid TCP port: empty (field left blank) or digits only, 1-65535.
// Number('143a') is NaN, but Number('') is 0 and Number(' 5 ') trims,
// so this checks the raw digits pattern first rather than trusting
// Number() alone.
function isValidPort(v: string): boolean {
  const trimmed = v.trim()
  if (trimmed === '') return true
  if (!/^\d+$/.test(trimmed)) return false
  const n = Number(trimmed)
  return n >= 1 && n <= 65535
}

// ConnectorAdd is preset-aware and its own page: MCP and github presets
// are created, tested, and enabled in one go with Add gated on a
// passing test (same contract as adding a provider); Google presets
// take the OAuth client and hand off to Google's consent screen
// instead - there is no unsaved test to run before that redirect.
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
  const [imapHost, setImapHost] = useState('')
  const [imapPort, setImapPort] = useState('')
  const [imapUsername, setImapUsername] = useState('')
  const [imapSMTPHost, setImapSMTPHost] = useState('')
  const [imapSMTPPort, setImapSMTPPort] = useState('')
  const [imapPassword, setImapPassword] = useState('')
  const [caldavURL, setCaldavURL] = useState('')
  const [caldavUsername, setCaldavUsername] = useState('')
  const [caldavPassword, setCaldavPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [test, setTest] = useState<{ ok: boolean; error?: string; identity?: GitHubIdentity } | null>(
    null,
  )
  // The connector row is created (disabled) as part of testing an MCP
  // or github preset - there's no unsaved-config validate endpoint like
  // providers have. createdID tracks that row so a passing test's
  // Add just enables it rather than creating a second one.
  const [createdID, setCreatedID] = useState<string | null>(null)
  // tokenCredMode/clientSecretCredMode: "new" pastes+stores a fresh
  // secret (current behavior); "existing" reuses a stored ref instead
  // - e.g. the same GitHub PAT already used by another connector, or
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
    setImapHost('')
    setImapPort('')
    setImapUsername('')
    setImapSMTPHost('')
    setImapSMTPPort('')
    setImapPassword('')
    setCaldavURL('')
    setCaldavUsername('')
    setCaldavPassword('')
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
  const isImap = preset.kind === 'imap'
  const isCalDAV = preset.kind === 'caldav'
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
    if (!isGitHub && !isImap && !isCalDAV && !endpoint.trim()) {
      toast.error('Endpoint required', { description: 'An MCP endpoint is required to test this connector.' })
      return
    }
    if (isImap && (!imapHost.trim() || !imapUsername.trim())) {
      toast.error('Host and username required', { description: 'An IMAP host and username are required to test this connector.' })
      return
    }
    if (isImap && (!isValidPort(imapPort) || !isValidPort(imapSMTPPort))) {
      toast.error('Invalid port', { description: 'Port must be a number between 1 and 65535.' })
      return
    }
    if (isCalDAV && (!caldavURL.trim() || !caldavUsername.trim())) {
      toast.error('Calendar URL and username required', {
        description: 'A calendar URL and username are required to test this connector.',
      })
      return
    }
    if (isGitHub && !usingExistingToken && !token.trim()) {
      toast.error('Token required', { description: 'A personal access token is required to test this connector.' })
      return
    }
    if (isImap && !usingExistingToken && !imapPassword.trim()) {
      toast.error('Password required', { description: 'A password is required to test this connector.' })
      return
    }
    if (isCalDAV && !usingExistingToken && !caldavPassword.trim()) {
      toast.error('Password required', { description: 'A password is required to test this connector.' })
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
          : isImap
            ? `${refBase}_IMAP_PASSWORD`
            : isCalDAV
              ? `${refBase}_CALDAV_PASSWORD`
              : refBase.endsWith('_MCP')
                ? `${refBase}_TOKEN`
                : `${refBase}_MCP_TOKEN`
      const secretValue = isImap ? imapPassword : isCalDAV ? caldavPassword : token
      if (!usingExistingToken && secretValue) await setSecret(tokenRef, secretValue.trim())
      const id = await createConnector(
        isGitHub
          ? { name: slug, kind: 'github', config: {}, credential_ref: tokenRef, enabled: false }
          : isImap
            ? {
                name: slug,
                kind: 'imap',
                config: {
                  host: imapHost.trim(),
                  ...(imapPort.trim() ? { port: Number(imapPort) } : {}),
                  username: imapUsername.trim(),
                  ...(imapSMTPHost.trim() ? { smtp_host: imapSMTPHost.trim() } : {}),
                  ...(imapSMTPPort.trim() ? { smtp_port: Number(imapSMTPPort) } : {}),
                },
                credential_ref: tokenRef,
                enabled: false,
              }
            : isCalDAV
              ? {
                  name: slug,
                  kind: 'caldav',
                  config: { url: caldavURL.trim(), username: caldavUsername.trim() },
                  credential_ref: tokenRef,
                  enabled: false,
                }
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
    (isGitHub
      ? usingExistingToken
        ? existingTokenRef !== ''
        : token.trim() !== ''
      : isImap
        ? imapHost.trim() !== '' &&
          imapUsername.trim() !== '' &&
          (usingExistingToken ? existingTokenRef !== '' : imapPassword.trim() !== '')
        : isCalDAV
          ? caldavURL.trim() !== '' &&
            caldavUsername.trim() !== '' &&
            (usingExistingToken ? existingTokenRef !== '' : caldavPassword.trim() !== '')
          : endpoint.trim() !== '')
  const canSubmitOAuth =
    slug !== '' &&
    clientID.trim() !== '' &&
    (usingExistingClientSecret ? existingClientSecretRef !== '' : clientSecret !== '')

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
        description={`kind: ${preset.kind}`}
        meta={<ConnectorLogo preset={preset} className="size-9" />}
        breadcrumbs={[
          { label: 'Settings', href: '/settings' },
          { label: area.label, href: '/settings/connectors' },
          { label: `Add ${preset.name}` },
        ]}
      />

      <Form onSubmit={(e) => e.preventDefault()}>
        <FieldGroup>
          <Field label="Name" description="lowercase slug, prefixes this connector's tool names">
            <Input
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                invalidate()
              }}
              placeholder={preset.id === 'custom-mcp' ? 'my-server' : slugify(preset.name)}
            />
          </Field>

          {isOAuth ? (
            <>
              <Field label="OAuth client ID">
                <Input
                  value={clientID}
                  onChange={(e) => setClientID(e.target.value)}
                  placeholder={isMicrosoft ? 'application (client) ID' : '….apps.googleusercontent.com'}
                />
              </Field>
              <CredentialField
                label="OAuth client secret"
                mode={clientSecretCredMode}
                onModeChange={setClientSecretCredMode}
                existingRef={existingClientSecretRef}
                onExistingRefChange={setExistingClientSecretRef}
                secretValue={clientSecret}
                onSecretValueChange={setClientSecret}
                secretPlaceholder={isMicrosoft ? 'client secret value' : 'GOCSPX-…'}
                defaultBackend={defaultBackend}
                refName={`${refBase}${oauthSecretSuffix}`}
              />
              <p className="text-sm text-muted-foreground">
                {isMicrosoft
                  ? 'From an Azure AD app registration (multitenant, "Accounts in any organizational directory and personal Microsoft accounts"). Add'
                  : 'From a Google Cloud OAuth client (Web application). Add'}{' '}
                <span className="font-mono">{window.location.origin}/v1/connectors/oauth/callback</span>{' '}
                to its {isMicrosoft ? 'redirect URIs (Web platform)' : 'authorized redirect URIs'}. Scopes:{' '}
                {preset.scopes?.map((s) => s.split('/').pop()).join(', ')}. Saving redirects you to{' '}
                {oauthProviderLabel} to consent.
              </p>
            </>
          ) : (
            <>
              {!isGitHub && !isImap && !isCalDAV && (
                <Field
                  label="Endpoint"
                  description={preset.endpointHint}
                >
                  <Input
                    value={endpoint}
                    onChange={(e) => {
                      setEndpoint(e.target.value)
                      invalidate()
                    }}
                    placeholder="https://…/mcp"
                  />
                </Field>
              )}
              {isImap && (
                <>
                  <div className="grid grid-cols-2 gap-5">
                    <Field label="IMAP host">
                      <Input
                        value={imapHost}
                        onChange={(e) => {
                          setImapHost(e.target.value)
                          invalidate()
                        }}
                        placeholder="imap.example.com"
                      />
                    </Field>
                    <Field label="Port" required={false}>
                      <Input
                        value={imapPort}
                        onChange={(e) => {
                          setImapPort(e.target.value)
                          invalidate()
                        }}
                        placeholder="993"
                      />
                    </Field>
                  </div>
                  <Field label="Username">
                    <Input
                      value={imapUsername}
                      onChange={(e) => {
                        setImapUsername(e.target.value)
                        invalidate()
                      }}
                      placeholder="me@example.com"
                    />
                  </Field>
                  <div className="grid grid-cols-2 gap-5">
                    <Field label="SMTP host" description="leave blank to disable sending" required={false}>
                      <Input
                        value={imapSMTPHost}
                        onChange={(e) => {
                          setImapSMTPHost(e.target.value)
                          invalidate()
                        }}
                        placeholder="smtp.example.com"
                      />
                    </Field>
                    <Field label="SMTP port" required={false}>
                      <Input
                        value={imapSMTPPort}
                        onChange={(e) => {
                          setImapSMTPPort(e.target.value)
                          invalidate()
                        }}
                        placeholder="587"
                      />
                    </Field>
                  </div>
                </>
              )}
              {isCalDAV && (
                <>
                  <Field label="Calendar URL" description="the calendar collection URL itself, no discovery">
                    <Input
                      value={caldavURL}
                      onChange={(e) => {
                        setCaldavURL(e.target.value)
                        invalidate()
                      }}
                      placeholder="https://cal.example.com/dav/calendars/user/personal/"
                    />
                  </Field>
                  <Field label="Username">
                    <Input
                      value={caldavUsername}
                      onChange={(e) => {
                        setCaldavUsername(e.target.value)
                        invalidate()
                      }}
                      placeholder="me@example.com"
                    />
                  </Field>
                </>
              )}

              <CredentialField
                label={
                  isGitHub
                    ? 'Personal access token'
                    : isImap || isCalDAV
                      ? 'Password'
                      : preset.id === 'custom-mcp'
                        ? 'Bearer token (optional)'
                        : 'Bearer token'
                }
                mode={tokenCredMode}
                onModeChange={(m) => {
                  setTokenCredMode(m)
                  invalidate()
                }}
                existingRef={existingTokenRef}
                onExistingRefChange={(v) => {
                  setExistingTokenRef(v)
                  invalidate()
                }}
                secretValue={isImap ? imapPassword : isCalDAV ? caldavPassword : token}
                onSecretValueChange={(v) => {
                  if (isImap) setImapPassword(v)
                  else if (isCalDAV) setCaldavPassword(v)
                  else setToken(v)
                  invalidate()
                }}
                secretPlaceholder={isImap || isCalDAV ? 'password' : (preset.tokenPlaceholder ?? 'token')}
                defaultBackend={defaultBackend}
                refName={isGitHub ? `${refBase}_GITHUB_PAT` : isImap ? `${refBase}_IMAP_PASSWORD` : isCalDAV ? `${refBase}_CALDAV_PASSWORD` : `${refBase}_MCP_TOKEN`}
              />
              {!isImap && !isCalDAV && tokenCredMode === 'new' && (
                <p className="-mt-2 text-sm text-muted-foreground">
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
                </p>
              )}

              {testState === 'gate' ? (
                <div className="flex flex-wrap items-center gap-3 rounded-md border border-border bg-muted/40 p-4 text-sm text-muted-foreground">
                  <span className="min-w-0 flex-1 font-medium">Not tested yet, run a test before adding.</span>
                  <Button size="sm" variant="test" disabled={busy || !canTest} onClick={() => void runTest()}>
                    Test connection
                  </Button>
                </div>
              ) : (
                <TestStatus
                  state={testState as 'testing' | 'ok' | 'failed'}
                  message={
                    testState === 'testing'
                      ? 'Testing connection…'
                      : testState === 'ok'
                        ? test?.identity
                          ? `${connectedAs(test.identity)}, ${test.identity.scopes}.`
                          : 'Connection OK, tools are servable.'
                        : `Connection failed: ${test?.error}. The connector was saved disabled, fix and retry.`
                  }
                  action={
                    testState !== 'testing' && (
                      <Button size="sm" variant="test" disabled={busy || !canTest} onClick={() => void runTest()}>
                        Test connection
                      </Button>
                    )
                  }
                />
              )}
            </>
          )}
        </FieldGroup>

        <FormActions>
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() => navigate('/settings/connectors')}
          >
            Cancel
          </Button>
          {isOAuth ? (
            <Button disabled={!canSubmitOAuth || busy} onClick={() => void submitOAuth()}>
              {busy ? 'Redirecting…' : `Save & connect ${oauthProviderLabel}`}
            </Button>
          ) : (
            <Button disabled={!tested || busy} onClick={() => void submit()}>
              Add connector
            </Button>
          )}
        </FormActions>
      </Form>
    </PageShell>
  )
}
