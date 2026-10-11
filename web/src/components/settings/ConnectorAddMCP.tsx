import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import {
  connectorOAuthStart,
  createConnector,
  listAgents,
  patchAgent,
  probeConnector,
  setSecret,
} from '../../api/client'
import type { AdminAgent, ConnectorProbe, ConnectorProbeTool } from '../../api/types'
import { Alert, AlertDescription } from '../ui/alert'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Checkbox } from '../ui/checkbox'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../ui/table'
import { Textarea } from '../ui/textarea'
import { Field, FieldGroup, Form, FormActions } from '../timothy/field'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { ConnectorLogo } from './ConnectorLogo'
import { presetFor } from './connectorPresets'
import {
  appendAllowlist,
  buildArgs,
  indexNote,
  lastProbeFrom,
  parseMCPInput,
  schemaFields,
  stdioIssueURL,
  stdioReason,
  type FieldValue,
} from './mcpAddFlow'
import { mcpOAuthRefs, refBaseFor, tokenRefFor } from './credentialRefs'
import { settingsArea } from './settingsAreas'
import { TestStatus } from './TestStatus'
import { errText, isTimothyAuthError } from '../../lib/errors'
import { useOnboarding } from '../../onboarding/context'

const area = settingsArea('connectors')
const preset = presetFor({ kind: 'mcp', config: {} })


// ConnectorAddMCP is the custom MCP tile's add flow (D-152, issue
// #1166): paste a URL or config, check the server before anything is
// saved, then choose tools and the agents that get them.
export function ConnectorAddMCP() {
  const navigate = useNavigate()
  const { refresh: refreshOnboarding } = useOnboarding()
  const [input, setInput] = useState('')
  const parsed = useMemo(() => parseMCPInput(input), [input])
  const [pick, setPick] = useState(0)
  const candidate = parsed.candidates[pick]
  const [name, setName] = useState('')
  const [token, setToken] = useState('')
  const [askToken, setAskToken] = useState(false)
  const [clientID, setClientID] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [probe, setProbe] = useState<ConnectorProbe | null>(null)
  const [probing, setProbing] = useState(false)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [agents, setAgents] = useState<AdminAgent[]>([])
  const [chosenAgents, setChosenAgents] = useState<Set<string>>(new Set())
  const [tryTool, setTryTool] = useState<ConnectorProbeTool | null>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    listAgents()
      .then(setAgents)
      .catch((err: unknown) => toast.error('Could not load agents', { description: errText(err) }))
  }, [])

  const choose = (text: string, index: number) => {
    const c = parseMCPInput(text).candidates[index]
    setInput(text)
    setPick(index)
    setName(c?.name ?? '')
    setToken(c?.token ?? '')
    setAskToken(false)
    setClientID('')
    setClientSecret('')
    setProbe(null)
    setTryTool(null)
  }

  const runProbe = async () => {
    if (!candidate) return
    setProbing(true)
    setProbe(null)
    setTryTool(null)
    try {
      const res = await probeConnector({
        name: name.trim(),
        endpoint: candidate.endpoint,
        ...(Object.keys(candidate.headers).length > 0 ? { headers: candidate.headers } : {}),
        ...(token.trim() ? { token: token.trim() } : {}),
      })
      setProbe(res)
      setSelected(new Set(res.tools.map((t) => t.name)))
      if (res.status === 'needs_token') setAskToken(true)
    } catch (err) {
      if (!isTimothyAuthError(err)) toast.error('Could not check the server', { description: errText(err) })
    } finally {
      setProbing(false)
    }
  }

  const save = async () => {
    if (!candidate || probe?.status !== 'ok') return
    setSaving(true)
    const connectorName = name.trim()
    try {
      const ref = token.trim() ? tokenRefFor('mcp', refBaseFor(connectorName)) : ''
      if (ref) await setSecret(ref, token.trim())
      const headers = candidate.headers
      await createConnector({
        name: connectorName,
        kind: 'mcp',
        config: {
          endpoint: candidate.endpoint,
          ...(Object.keys(headers).length > 0 ? { headers } : {}),
          last_probe: lastProbeFrom(probe),
        },
        credential_ref: ref,
        enabled: true,
      })
    } catch (err) {
      toast.error('Could not add connector', { description: errText(err) })
      setSaving(false)
      return
    }
    const names = probe.tools.filter((t) => selected.has(t.name)).map((t) => t.final_name || t.name)
    const failed: string[] = []
    if (names.length > 0) {
      for (const agent of agents.filter((a) => chosenAgents.has(a.id))) {
        try {
          await patchAgent(agent.id, { tools: appendAllowlist(agent.tools, names) })
        } catch {
          failed.push(agent.name)
        }
      }
    }
    if (failed.length > 0) {
      toast.error('Connector added, but some agents were not updated', {
        description: `Add the tools to ${failed.join(', ')} from Agents settings.`,
      })
    } else {
      toast.success('Connector added', { description: `${connectorName} is connected and its tools are servable.` })
    }
    void refreshOnboarding()
    navigate('/settings/connectors')
  }

  // connectOAuth creates the connector disabled in oauth mode and hands
  // off to the server's consent page, as an OAuth-mode MCP preset does.
  const connectOAuth = async () => {
    if (!candidate || probe?.status !== 'needs_oauth') return
    setSaving(true)
    const refs = mcpOAuthRefs(refBaseFor(name))
    const pastedClient = clientID.trim() !== ''
    const headers = candidate.headers
    try {
      if (pastedClient && clientSecret) await setSecret(refs.clientSecret, clientSecret)
      const id = await createConnector({
        name: name.trim(),
        kind: 'mcp',
        config: {
          endpoint: candidate.endpoint,
          ...(Object.keys(headers).length > 0 ? { headers } : {}),
          auth_mode: 'oauth',
          ...(pastedClient ? { client_id: clientID.trim() } : {}),
          ...(pastedClient && clientSecret ? { client_secret_ref: refs.clientSecret } : {}),
        },
        credential_ref: refs.tokens,
        enabled: false,
      })
      window.location.assign(await connectorOAuthStart(id))
    } catch (err) {
      toast.error('Could not connect MCP server', { description: errText(err) })
      setSaving(false)
    }
  }

  const needsOAuth = probe?.status === 'needs_oauth'
  const httpsEndpoint = candidate?.endpoint.startsWith('https://') === true
  const canConnectOAuth =
    needsOAuth && name.trim() !== '' && httpsEndpoint && (clientSecret === '' || clientID.trim() !== '')

  const toggle = <T,>(set: Set<T>, item: T, on: boolean): Set<T> => {
    const next = new Set(set)
    if (on) next.add(item)
    else next.delete(item)
    return next
  }

  return (
    <PageShell>
      <PageHeader
        title={`Add ${preset.name}`}
        description="kind: mcp"
        meta={<ConnectorLogo preset={preset} className="size-9" />}
        breadcrumbs={[
          { label: 'Settings', href: '/settings' },
          { label: area.label, href: '/settings/connectors' },
          { label: `Add ${preset.name}` },
        ]}
      />

      <Form onSubmit={(e) => e.preventDefault()}>
        <FieldGroup title="1. Server" description="Paste the server's URL, or the JSON config its provider publishes.">
          <Field label="URL or JSON config">
            <Textarea
              value={input}
              onChange={(e) => choose(e.target.value, 0)}
              placeholder={'https://mcp.example.com/mcp\nor {"mcpServers": {"example": {"url": "https://mcp.example.com/mcp"}}}'}
              className="font-mono text-xs"
            />
          </Field>
          {parsed.error && <p className="text-sm text-destructive">{parsed.error}</p>}
          {parsed.unsupported.length > 0 && (
            <Alert tone="warning">
              <AlertDescription>
                <ul className="space-y-1">
                  {parsed.unsupported.map((u) => (
                    <li key={u.name}>
                      <span className="font-mono">{u.name || 'entry'}</span>: {u.reason}{' '}
                      {u.reason === stdioReason && (
                        <a href={stdioIssueURL} target="_blank" rel="noreferrer" className="underline underline-offset-2">
                          Track stdio support
                        </a>
                      )}
                    </li>
                  ))}
                </ul>
              </AlertDescription>
            </Alert>
          )}
          {parsed.candidates.length > 1 && (
            <Field label="Server" description="this config has several HTTP servers; add one at a time">
              {(props) => (
                <Select value={String(pick)} onValueChange={(v) => v && choose(input, Number(v))}>
                  <SelectTrigger id={props.id} className="w-full" aria-label="Server">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {parsed.candidates.map((c, i) => (
                      <SelectItem key={`${c.name}-${i}`} value={String(i)}>
                        {c.name || c.endpoint}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
          )}
          {candidate && (
            <>
              <p className="text-sm text-muted-foreground">
                Endpoint: <span className="font-mono">{candidate.endpoint}</span>
                {Object.keys(candidate.headers).length > 0 && (
                  <> · headers: <span className="font-mono">{Object.keys(candidate.headers).join(', ')}</span></>
                )}
              </p>
              {candidate.placeholders.length > 0 && (
                <Alert tone="warning">
                  <AlertDescription>
                    Replace the template value of {candidate.placeholders.join(', ')} in the config above before checking.
                  </AlertDescription>
                </Alert>
              )}
              <Field label="Name" description="unique, identifies this connector as an account option">
                <Input
                  value={name}
                  onChange={(e) => {
                    setName(e.target.value)
                    setProbe(null)
                  }}
                  placeholder="my-server"
                />
              </Field>
              {(askToken || token !== '') && (
                <Field label="Bearer token" description="stored as a secret when you add the connector">
                  <Input
                    type="password"
                    value={token}
                    onChange={(e) => {
                      setToken(e.target.value)
                      setProbe(null)
                    }}
                    placeholder="token"
                    autoComplete="off"
                  />
                </Field>
              )}
              <div>
                <Button
                  type="button"
                  variant="test"
                  disabled={probing || !name.trim() || candidate.placeholders.length > 0}
                  onClick={() => void runProbe()}
                >
                  {probe ? 'Check again' : 'Check server'}
                </Button>
              </div>
            </>
          )}
        </FieldGroup>

        {probing && <TestStatus state="testing" message="Checking server…" />}
        {probe && <ProbeOutcome probe={probe} hadToken={token.trim() !== ''} />}

        {needsOAuth && (
          <FieldGroup title="2. OAuth login" description="This server signs in through its own authorization server.">
            <Field label="Client ID" required={false}>
              <Input value={clientID} onChange={(e) => setClientID(e.target.value)} placeholder="client id" />
            </Field>
            <Field label="Client secret" required={false}>
              <Input
                type="password"
                value={clientSecret}
                onChange={(e) => setClientSecret(e.target.value)}
                placeholder="client secret"
                autoComplete="off"
              />
            </Field>
            <p className="-mt-2 text-sm text-muted-foreground">
              Only for servers without automatic client registration. Register{' '}
              <span className="font-mono">{window.location.origin}/v1/connectors/oauth/callback</span> as its redirect
              URI.
            </p>
            {!httpsEndpoint && <p className="text-sm text-destructive">OAuth login needs an https endpoint.</p>}
            <p className="text-sm text-muted-foreground">
              Connecting redirects you to the server's sign-in page to consent. Its tools are listed only after that,
              so add them to agent allowlists under Agents once the connector is connected.
            </p>
          </FieldGroup>
        )}

        {probe?.status === 'ok' && (
          <FieldGroup title="2. Tools" description="Checked tools are added to the agents you pick below.">
            <p className="text-sm text-muted-foreground">
              {indexNote(probe.tool_count, probe.index_threshold)}
              {probe.tool_count > probe.tools.length && ` Showing the first ${probe.tools.length}.`}
            </p>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-8">
                    <span className="sr-only">Allow</span>
                  </TableHead>
                  <TableHead>Tool</TableHead>
                  <TableHead>Description</TableHead>
                  <TableHead>Read-only (server claim)</TableHead>
                  <TableHead>
                    <span className="sr-only">Try it</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {probe.tools.map((t) => (
                  <TableRow key={t.name}>
                    <TableCell>
                      <Checkbox
                        aria-label={`Allow ${t.name}`}
                        checked={selected.has(t.name)}
                        onCheckedChange={(v) => setSelected((s) => toggle(s, t.name, v === true))}
                      />
                    </TableCell>
                    <TableCell className="font-mono text-xs">
                      {t.name}
                      {t.final_name && t.final_name !== t.name && (
                        <div className="text-muted-foreground">as {t.final_name}</div>
                      )}
                    </TableCell>
                    <TableCell className="max-w-md text-xs whitespace-normal text-muted-foreground">{t.description}</TableCell>
                    <TableCell>
                      {t.read_only_hint === null ? (
                        <span className="text-xs text-muted-foreground">not stated</span>
                      ) : t.read_only_hint ? (
                        <Badge variant="info">read-only</Badge>
                      ) : (
                        <Badge variant="warning">writes</Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      <Button type="button" size="sm" variant="ghost" onClick={() => setTryTool(t)}>
                        Try it
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            {tryTool && <TryIt key={tryTool.name} tool={tryTool} onClose={() => setTryTool(null)} />}
          </FieldGroup>
        )}

        {probe?.status === 'ok' && (
          <FieldGroup title="3. Agents" description="Each picked agent gets the checked tools on its tools allowlist.">
            {agents.length === 0 ? (
              <p className="text-sm text-muted-foreground">No agents yet. Add tools to an agent later from Agents settings.</p>
            ) : (
              <div className="space-y-2">
                {agents.map((a) => (
                  <label key={a.id} className="flex items-center gap-3 text-sm">
                    <Checkbox
                      aria-label={`Give tools to ${a.name}`}
                      checked={chosenAgents.has(a.id)}
                      onCheckedChange={(v) => setChosenAgents((s) => toggle(s, a.id, v === true))}
                    />
                    <span>{a.name}</span>
                  </label>
                ))}
              </div>
            )}
          </FieldGroup>
        )}

        <FormActions>
          <Button type="button" variant="outline" disabled={saving} onClick={() => navigate('/settings/connectors')}>
            Cancel
          </Button>
          {needsOAuth ? (
            <Button type="button" disabled={saving || !canConnectOAuth} onClick={() => void connectOAuth()}>
              {saving ? 'Redirecting…' : 'Connect with OAuth'}
            </Button>
          ) : (
            <Button
              type="button"
              disabled={saving || probe?.status !== 'ok' || !name.trim()}
              onClick={() => void save()}
            >
              Add connector
            </Button>
          )}
        </FormActions>
      </Form>
    </PageShell>
  )
}

function ProbeOutcome({ probe, hadToken }: { probe: ConnectorProbe; hadToken: boolean }) {
  switch (probe.status) {
    case 'ok':
      return (
        <TestStatus
          state="ok"
          message={`Connected to ${probe.server.name || 'the server'}${probe.server.version ? ` ${probe.server.version}` : ''}. Nothing is saved yet.`}
        />
      )
    case 'needs_token':
      return (
        <Alert tone="warning">
          <AlertDescription>
            {hadToken
              ? 'The server rejected this token. Check it and try again.'
              : 'The server needs a token. Paste it above and check again.'}
          </AlertDescription>
        </Alert>
      )
    case 'needs_oauth':
      return null
    case 'unreachable':
      return <TestStatus state="failed" message="Could not reach the server." detail={probe.message} />
    default:
      return <TestStatus state="failed" message="The server answered, but the check failed." detail={probe.message} />
  }
}

// TryIt is a dry run: settings never executes a tool, so every real
// call still goes through chat's permission prompt. It validates the
// arguments against the tool's schema and shows the request a call
// would send.
function TryIt({ tool, onClose }: { tool: ConnectorProbeTool; onClose: () => void }) {
  const fields = useMemo(() => schemaFields(tool.input_schema), [tool.input_schema])
  const [values, setValues] = useState<Record<string, FieldValue>>({})
  const [result, setResult] = useState<{ errors: Record<string, string>; request?: string } | null>(null)

  const build = () => {
    const { args, errors } = buildArgs(fields, values)
    const ok = Object.keys(errors).length === 0
    setResult({
      errors,
      request: ok
        ? JSON.stringify({ jsonrpc: '2.0', method: 'tools/call', params: { name: tool.name, arguments: args } }, null, 2)
        : undefined,
    })
  }

  return (
    <div className="space-y-4 rounded-md border border-border p-4">
      <div className="flex items-center justify-between gap-3">
        <h3 className="font-mono text-sm font-semibold">{tool.name}</h3>
        <Button type="button" size="sm" variant="ghost" onClick={onClose}>
          Close
        </Button>
      </div>
      <p className="text-sm text-muted-foreground">
        Dry run. Settings does not run tools: a real call happens in chat and goes through the permission prompt. This
        checks your arguments and shows the request Timothy would send.
      </p>
      {fields.length === 0 && <p className="text-sm text-muted-foreground">This tool takes no arguments.</p>}
      {fields.map((f) => {
        const key = f.path.join('.')
        const error = result?.errors[key]
        const set = (v: FieldValue) => setValues((s) => ({ ...s, [key]: v }))
        return (
          <Field key={key} label={key} description={f.description} required={f.required} error={error}>
            {(props) =>
              f.kind === 'boolean' ? (
                <Checkbox id={props.id} checked={values[key] === true} onCheckedChange={(v) => set(v === true)} />
              ) : f.kind === 'enum' ? (
                <Select value={typeof values[key] === 'string' ? values[key] : ''} onValueChange={(v) => v && set(v)}>
                  <SelectTrigger id={props.id} className="w-full" aria-label={key}>
                    <SelectValue placeholder="Choose a value" />
                  </SelectTrigger>
                  <SelectContent>
                    {f.options?.map((o) => (
                      <SelectItem key={String(o)} value={String(o)}>
                        {String(o)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : f.kind === 'json' ? (
                <Textarea
                  {...props}
                  className="font-mono text-xs"
                  value={typeof values[key] === 'string' ? values[key] : ''}
                  onChange={(e) => set(e.target.value)}
                  placeholder="JSON value"
                />
              ) : (
                <Input
                  {...props}
                  inputMode={f.kind === 'string' ? undefined : 'decimal'}
                  value={typeof values[key] === 'string' ? values[key] : ''}
                  onChange={(e) => set(e.target.value)}
                />
              )
            }
          </Field>
        )
      })}
      <Button type="button" size="sm" variant="test" onClick={build}>
        Check arguments
      </Button>
      {result?.request && (
        <pre className="overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs" aria-label="Request preview">
          {result.request}
        </pre>
      )}
    </div>
  )
}
