import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { listAgents, patchAgent, probeConnector } from '../../api/client'
import type { AdminAgent, AdminConnector, ConnectorProbe, MCPProbedTool } from '../../api/types'
import { Alert, AlertDescription } from '../ui/alert'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Checkbox } from '../ui/checkbox'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '../ui/table'
import { Panel } from '../timothy/panel'
import { allowlistName, appendAllowlist, lastProbeOf } from './mcpAddFlow'
import { TestStatus } from './TestStatus'
import { errText, isTimothyAuthError } from '../../lib/errors'
import { relativeTime } from '../../lib/format'

// ConnectorMCPTools is the connector page's Tools panel (issue #1191):
// the tools the last probe recorded, which agents allow each one, and
// a Re-probe that lists what the server added or removed since.
export function ConnectorMCPTools({
  connector,
  onProbed,
  reconnect,
  reconnecting,
}: {
  connector: AdminConnector
  // Called after a successful re-probe so the page reloads the stored
  // record before any later save carries the config along.
  onProbed: () => Promise<unknown>
  // OAuth-mode connectors only: starts the sign-in again.
  reconnect?: () => void | Promise<void>
  reconnecting?: boolean
}) {
  const stored = lastProbeOf(connector)
  const [tools, setTools] = useState<MCPProbedTool[]>(stored?.tools ?? [])
  const [toolCount, setToolCount] = useState(stored?.tool_count ?? 0)
  const [probedAt, setProbedAt] = useState(stored?.at ?? null)
  const [added, setAdded] = useState<Set<string>>(new Set())
  const [removed, setRemoved] = useState<MCPProbedTool[]>([])
  const [outcome, setOutcome] = useState<ConnectorProbe | null>(null)
  const [probing, setProbing] = useState(false)
  const [agents, setAgents] = useState<AdminAgent[]>([])
  const [busy, setBusy] = useState<string | null>(null)

  useEffect(() => {
    listAgents()
      .then((list) => setAgents(list.filter((a) => a.enabled)))
      .catch((err: unknown) => {
        if (!isTimothyAuthError(err)) toast.error('Could not load agents', { description: errText(err) })
      })
  }, [])

  const reprobe = async () => {
    setProbing(true)
    try {
      const res = await probeConnector({ connector_id: connector.id })
      setOutcome(res)
      if (res.status !== 'ok') return
      const gone = new Set(res.removed ?? [])
      setRemoved(tools.filter((t) => gone.has(t.name)))
      setAdded(new Set(res.added ?? []))
      setTools(res.tools.map((t) => ({ name: t.name, final_name: t.final_name, read_only_hint: t.read_only_hint })))
      setToolCount(res.tool_count)
      setProbedAt(new Date().toISOString())
      await onProbed()
    } catch (err) {
      if (!isTimothyAuthError(err)) toast.error('Could not check the server', { description: errText(err) })
    } finally {
      setProbing(false)
    }
  }

  const setAllowed = async (agent: AdminAgent, tool: MCPProbedTool, on: boolean) => {
    const name = allowlistName(tool)
    const next = on ? appendAllowlist(agent.tools, [name]) : agent.tools.filter((n) => n !== name)
    setBusy(`${agent.id}:${name}`)
    try {
      await patchAgent(agent.id, { tools: next })
      setAgents((list) => list.map((a) => (a.id === agent.id ? { ...a, tools: next } : a)))
    } catch (err) {
      if (!isTimothyAuthError(err)) toast.error(`Could not update ${agent.name}`, { description: errText(err) })
    } finally {
      setBusy(null)
    }
  }

  const rows: { tool: MCPProbedTool; state: 'current' | 'added' | 'removed' }[] = [
    ...tools.map((tool) => ({ tool, state: added.has(tool.name) ? ('added' as const) : ('current' as const) })),
    ...removed.map((tool) => ({ tool, state: 'removed' as const })),
  ]

  return (
    <Panel
      title="Tools"
      description={probedAt ? `Last checked ${relativeTime(probedAt)}.` : 'Not checked yet.'}
      actions={
        <Button size="sm" variant="test" disabled={probing} onClick={() => void reprobe()}>
          {probing ? 'Checking…' : 'Re-probe'}
        </Button>
      }
    >
      <div className="space-y-3">
        {outcome && outcome.status !== 'ok' && (
          <ProbeFailure outcome={outcome} reconnect={reconnect} reconnecting={reconnecting} />
        )}
        {outcome?.status === 'ok' && (
          <TestStatus
            state="ok"
            message={
              added.size === 0 && removed.length === 0
                ? 'Tool list unchanged.'
                : `${added.size} added, ${removed.length} removed since the last check.`
            }
          />
        )}
        {rows.length === 0 ? (
          <p className="text-sm text-muted-foreground">No tools recorded yet. Press Re-probe to list the server's tools.</p>
        ) : (
          <>
            {toolCount > tools.length && (
              <p className="text-sm text-muted-foreground">
                Showing the first {tools.length} of {toolCount} tools.
              </p>
            )}
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Tool</TableHead>
                  <TableHead>Read-only (server claim)</TableHead>
                  <TableHead>Agents</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map(({ tool, state }) => {
                  const name = allowlistName(tool)
                  return (
                    <TableRow key={`${state}-${tool.name}`} className={state === 'removed' ? 'opacity-60' : undefined}>
                      <TableCell className="font-mono text-xs">
                        <span className="inline-flex items-center gap-2">
                          {tool.name}
                          {state === 'added' && <Badge variant="good">new</Badge>}
                          {state === 'removed' && <Badge variant="neutral">removed</Badge>}
                        </span>
                        {tool.final_name && tool.final_name !== tool.name && (
                          <div className="text-muted-foreground">as {tool.final_name}</div>
                        )}
                      </TableCell>
                      <TableCell>
                        {tool.read_only_hint === null ? (
                          <span className="text-xs text-muted-foreground">not stated</span>
                        ) : tool.read_only_hint ? (
                          <Badge variant="info">read-only</Badge>
                        ) : (
                          <Badge variant="warning">writes</Badge>
                        )}
                      </TableCell>
                      <TableCell>
                        {agents.length === 0 ? (
                          <span className="text-xs text-muted-foreground">no agents</span>
                        ) : (
                          <div className="flex flex-wrap gap-x-4 gap-y-1">
                            {agents.map((a) => (
                              <label key={a.id} className="flex items-center gap-2 text-xs">
                                <Checkbox
                                  aria-label={`Allow ${tool.name} for ${a.name}`}
                                  checked={a.tools.includes(name)}
                                  disabled={state === 'removed' || busy === `${a.id}:${name}`}
                                  onCheckedChange={(v) => void setAllowed(a, tool, v === true)}
                                />
                                <span>{a.name}</span>
                              </label>
                            ))}
                          </div>
                        )}
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </>
        )}
      </div>
    </Panel>
  )
}

function ProbeFailure({
  outcome,
  reconnect,
  reconnecting,
}: {
  outcome: ConnectorProbe
  reconnect?: () => void | Promise<void>
  reconnecting?: boolean
}) {
  switch (outcome.status) {
    case 'needs_oauth':
      return (
        <Alert tone="warning">
          <AlertDescription>
            <div className="flex flex-wrap items-center gap-3">
              <span className="min-w-0 flex-1">
                {reconnect
                  ? 'The sign-in for this server expired or was revoked. Reconnect to list its tools.'
                  : 'This server needs an OAuth sign-in. Edit the connector and connect with OAuth.'}
              </span>
              {reconnect && (
                <Button size="sm" variant="outline" disabled={reconnecting} onClick={() => void reconnect()}>
                  {reconnecting ? 'Redirecting…' : 'Reconnect'}
                </Button>
              )}
            </div>
          </AlertDescription>
        </Alert>
      )
    case 'needs_token':
      return (
        <Alert tone="warning">
          <AlertDescription>The server rejected the stored token. Rotate it in the Connection panel.</AlertDescription>
        </Alert>
      )
    case 'unreachable':
      return <TestStatus state="failed" message="Could not reach the server." detail={outcome.message} />
    default:
      return <TestStatus state="failed" message="The server answered, but the check failed." detail={outcome.message} />
  }
}
