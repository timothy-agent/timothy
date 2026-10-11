import type { AdminConnector, ConnectorProbe, MCPLastProbe, MCPMissionReads, MCPProbedTool } from '../../api/types'
import { slugify } from '../../lib/slugify'

// MCPCandidate is one server entry the add flow can probe: an HTTP
// endpoint, or a local (stdio) server when command is set (D-162).
// token is a literal bearer lifted out of an Authorization header so it
// is stored as a secret, never as a plain config header.
export interface MCPCandidate {
  name: string
  endpoint: string
  headers: Record<string, string>
  token: string
  // placeholders names headers whose value still reads like a template
  // (${VAR}, <token>); the operator fills them in before probing.
  placeholders: string[]
  // command, args and env describe a local server. env values are what
  // the config carried; a template value is left empty to fill in.
  command?: string
  args?: string[]
  env?: Record<string, string>
}

export interface MCPUnsupported {
  name: string
  reason: string
}

export interface ParsedMCPInput {
  candidates: MCPCandidate[]
  unsupported: MCPUnsupported[]
  error?: string
}

const placeholderPattern = /\$\{[^}]*\}|<[^>]*>|\{\{[^}]*\}\}/

function isHTTP(v: unknown): v is string {
  return typeof v === 'string' && /^https?:\/\/\S+$/i.test(v.trim())
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function parseLocal(name: string, raw: Record<string, unknown>): MCPCandidate | MCPUnsupported {
  if (typeof raw.command !== 'string' || raw.command.trim() === '') return { name, reason: 'The command must be a string.' }
  const args = Array.isArray(raw.args) ? raw.args.filter((a): a is string => typeof a === 'string') : []
  const env: Record<string, string> = {}
  if (isRecord(raw.env)) {
    for (const [k, v] of Object.entries(raw.env)) {
      if (typeof v === 'string') env[k] = placeholderPattern.test(v) ? '' : v
    }
  }
  return { name: slugify(name), endpoint: '', headers: {}, token: '', placeholders: [], command: raw.command.trim(), args, env }
}

function parseEntry(name: string, raw: unknown): MCPCandidate | MCPUnsupported {
  if (!isRecord(raw)) return { name, reason: 'Not a server entry.' }
  if (raw.command !== undefined) return parseLocal(name, raw)
  const endpoint = raw.url ?? raw.serverUrl ?? raw.endpoint
  if (!isHTTP(endpoint)) return { name, reason: 'No http or https URL in this entry.' }
  const headers: Record<string, string> = {}
  const placeholders: string[] = []
  let token = ''
  if (isRecord(raw.headers)) {
    for (const [k, v] of Object.entries(raw.headers)) {
      if (typeof v !== 'string') continue
      const bearer = k.toLowerCase() === 'authorization' ? /^Bearer\s+(.+)$/i.exec(v.trim()) : null
      if (bearer) {
        // A templated bearer becomes an empty token field to fill in.
        if (!placeholderPattern.test(bearer[1])) token = bearer[1]
        continue
      }
      if (placeholderPattern.test(v)) placeholders.push(k)
      headers[k] = v
    }
  }
  return { name: slugify(name), endpoint: endpoint.trim(), headers, token, placeholders }
}

// parseMCPInput reads the add flow's one input: a bare URL, a
// {"mcpServers": {...}} config, the inner name-to-entry object, or a
// single entry. Entries with a command become local server candidates.
export function parseMCPInput(text: string): ParsedMCPInput {
  const trimmed = text.trim()
  if (trimmed === '') return { candidates: [], unsupported: [] }
  if (isHTTP(trimmed)) {
    return { candidates: [{ name: '', endpoint: trimmed, headers: {}, token: '', placeholders: [] }], unsupported: [] }
  }
  let doc: unknown
  try {
    doc = JSON.parse(trimmed)
  } catch {
    return { candidates: [], unsupported: [], error: 'Paste an http(s) URL or a JSON config.' }
  }
  if (!isRecord(doc)) return { candidates: [], unsupported: [], error: 'The JSON must be an object.' }
  const isEntry = ['url', 'serverUrl', 'endpoint', 'command'].some((k) => k in doc)
  const servers = isEntry ? { '': doc } : isRecord(doc.mcpServers) ? doc.mcpServers : doc
  const out: ParsedMCPInput = { candidates: [], unsupported: [] }
  for (const [name, raw] of Object.entries(servers)) {
    const entry = parseEntry(name, raw)
    if ('endpoint' in entry) out.candidates.push(entry)
    else out.unsupported.push(entry)
  }
  if (out.candidates.length === 0 && out.unsupported.length === 0) out.error = 'No servers found in this JSON.'
  return out
}

// manualLocalCandidate is the local server typed into the Advanced form:
// arguments one per line, env var names one per line or comma
// separated, values typed separately.
export function manualLocalCandidate(command: string, argsText: string, envNamesText: string): MCPCandidate | null {
  if (command.trim() === '') return null
  const args = argsText
    .split('\n')
    .map((a) => a.trim())
    .filter((a) => a !== '')
  const env: Record<string, string> = {}
  for (const n of envNamesText.split(/[\n,]/)) if (n.trim() !== '') env[n.trim()] = ''
  return { name: '', endpoint: '', headers: {}, token: '', placeholders: [], command: command.trim(), args, env }
}

// envFor collects the typed env values for names, trimmed.
export function envFor(names: string[], values: Record<string, string>): Record<string, string> {
  return Object.fromEntries(names.map((n) => [n, (values[n] ?? '').trim()]))
}

// envNameError checks one env var name the way the server does; the
// bridge token, PATH and HOME are set by Timothy.
export function envNameError(name: string): string | undefined {
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) return 'Letters, digits and underscores, not starting with a digit.'
  if (name === 'PATH' || name === 'HOME' || name === 'MCP_BRIDGE_TOKEN') return `${name} is set by Timothy.`
  return undefined
}

// LocalServer is a command connector's saved config, read for display.
export interface LocalServer {
  command: string
  args: string[]
  envRefs: Record<string, string>
  internet: boolean
}

// localServerOf reads a command connector's config; null for an
// endpoint connector.
export function localServerOf(connector: AdminConnector): LocalServer | null {
  const c = connector.config
  if (connector.kind !== 'mcp' || typeof c.command !== 'string' || c.command === '') return null
  const envRefs: Record<string, string> = {}
  if (isRecord(c.env_refs)) for (const [k, v] of Object.entries(c.env_refs)) if (typeof v === 'string') envRefs[k] = v
  return {
    command: c.command,
    args: Array.isArray(c.args) ? c.args.filter((a): a is string => typeof a === 'string') : [],
    envRefs,
    internet: c.internet === true,
  }
}

// commandLine renders a command and its arguments for display, quoting
// arguments with spaces.
export function commandLine(command: string, args: string[]): string {
  return [command, ...args].map((a) => (/\s/.test(a) || a === '' ? JSON.stringify(a) : a)).join(' ')
}

// appendAllowlist adds names to an agent's tools allowlist, keeping
// order and skipping names already present.
export function appendAllowlist(current: string[], names: string[]): string[] {
  const out = [...current]
  for (const n of names) if (!out.includes(n)) out.push(n)
  return out
}

// lastProbeFrom is the config.last_probe record a successful probe
// leaves on the connector, so its page lists the tools right after
// adding.
export function lastProbeFrom(probe: ConnectorProbe, at = new Date()): MCPLastProbe {
  return {
    at: at.toISOString(),
    tool_count: probe.tool_count,
    ...(probe.hash ? { hash: probe.hash } : {}),
    tools: probe.tools.map((t) => ({
      name: t.name,
      ...(t.final_name ? { final_name: t.final_name } : {}),
      read_only_hint: t.read_only_hint,
    })),
  }
}

// lastProbeOf reads config.last_probe off a connector; null when absent
// or malformed.
export function lastProbeOf(connector: AdminConnector): MCPLastProbe | null {
  const raw = connector.config.last_probe
  if (!isRecord(raw) || typeof raw.at !== 'string' || !Array.isArray(raw.tools)) return null
  const tools: MCPProbedTool[] = []
  for (const t of raw.tools) {
    if (!isRecord(t) || typeof t.name !== 'string') continue
    tools.push({
      name: t.name,
      ...(typeof t.final_name === 'string' ? { final_name: t.final_name } : {}),
      read_only_hint: typeof t.read_only_hint === 'boolean' ? t.read_only_hint : null,
    })
  }
  return {
    at: raw.at,
    tool_count: typeof raw.tool_count === 'number' ? raw.tool_count : tools.length,
    ...(typeof raw.hash === 'string' && raw.hash ? { hash: raw.hash } : {}),
    tools,
  }
}

// missionReadsOf reads config.mission_reads; null when absent or
// malformed.
export function missionReadsOf(connector: AdminConnector): MCPMissionReads | null {
  const raw = connector.config.mission_reads
  if (!isRecord(raw) || typeof raw.hash !== 'string' || !Array.isArray(raw.tools)) return null
  return { hash: raw.hash, tools: raw.tools.filter((t): t is string => typeof t === 'string') }
}

// allowlistName is the name a tool carries on an agent's allowlist.
export function allowlistName(tool: MCPProbedTool): string {
  return tool.final_name || tool.name
}

// providerPattern is a valid MCP provider prefix (D-160).
export const providerPattern = /^[a-z0-9_]{1,32}$/

// allowedBy names the allowlist entry that admits tool, using the
// backend's rule: an exact match, or the tool ending in "_" + entry
// (so "search" admits notion_search). undefined when none does.
export function allowedBy(allowlist: string[], tool: string): string | undefined {
  if (allowlist.includes(tool)) return tool
  return allowlist.find((entry) => tool.endsWith(`_${entry}`))
}

// catalogProvider is the provider a preset tile saves: its id for a
// hosted MCP server, none for the custom tile, whose add flow asks.
export function catalogProvider(preset: { id: string; kind: string }): string | undefined {
  return preset.kind === 'mcp' && preset.id !== 'custom-mcp' ? preset.id : undefined
}

// indexNote explains whether chat sees the tools directly or through
// the deferred index, mirroring the backend's count > threshold rule.
export function indexNote(count: number, threshold: number): string {
  if (threshold <= 0) return `${count} tools. The tool index is off, so chat sees every tool directly.`
  if (count > threshold) {
    return `${count} tools, above the MCP tool index threshold of ${threshold}. Chat sees an index and loads each tool when it needs it.`
  }
  return `${count} tools, within the MCP tool index threshold of ${threshold}. Chat sees every tool directly.`
}

// SchemaField is one input of the Try it form, flattened from a tool's
// input schema; nested objects become dotted paths.
export interface SchemaField {
  path: string[]
  kind: 'string' | 'number' | 'integer' | 'boolean' | 'enum' | 'json'
  required: boolean
  options?: unknown[]
  description?: string
}

export function schemaFields(schema: unknown, path: string[] = [], parentRequired = true): SchemaField[] {
  if (!isRecord(schema) || !isRecord(schema.properties)) return []
  const required = Array.isArray(schema.required) ? schema.required : []
  const out: SchemaField[] = []
  for (const [key, prop] of Object.entries(schema.properties)) {
    const p = isRecord(prop) ? prop : {}
    const fieldPath = [...path, key]
    const req = parentRequired && required.includes(key)
    const description = typeof p.description === 'string' ? p.description : undefined
    if (p.type === 'object' && isRecord(p.properties)) {
      out.push(...schemaFields(p, fieldPath, req))
    } else if (Array.isArray(p.enum)) {
      out.push({ path: fieldPath, kind: 'enum', required: req, options: p.enum, description })
    } else if (p.type === 'string' || p.type === 'number' || p.type === 'integer' || p.type === 'boolean') {
      out.push({ path: fieldPath, kind: p.type, required: req, description })
    } else {
      out.push({ path: fieldPath, kind: 'json', required: req, description })
    }
  }
  return out
}

export type FieldValue = string | boolean

// buildArgs turns Try it form values into tool arguments, checking each
// field against its schema kind. Empty optional fields are left out.
export function buildArgs(
  fields: SchemaField[],
  values: Record<string, FieldValue>,
): { args: Record<string, unknown>; errors: Record<string, string> } {
  const args: Record<string, unknown> = {}
  const errors: Record<string, string> = {}
  for (const f of fields) {
    const key = f.path.join('.')
    const raw = values[key]
    let value: unknown
    if (f.kind === 'boolean') {
      if (raw === undefined && !f.required) continue
      value = raw === true
    } else {
      const text = typeof raw === 'string' ? raw.trim() : ''
      if (text === '') {
        if (f.required) errors[key] = 'Required.'
        continue
      }
      if (f.kind === 'number' || f.kind === 'integer') {
        value = Number(text)
        if (Number.isNaN(value) || (f.kind === 'integer' && !Number.isInteger(value))) {
          errors[key] = f.kind === 'integer' ? 'Must be a whole number.' : 'Must be a number.'
          continue
        }
      } else if (f.kind === 'enum') {
        value = f.options?.find((o) => String(o) === text)
        if (value === undefined) {
          errors[key] = 'Pick one of the listed values.'
          continue
        }
      } else if (f.kind === 'json') {
        try {
          value = JSON.parse(text)
        } catch {
          errors[key] = 'Must be valid JSON.'
          continue
        }
      } else {
        value = text
      }
    }
    let target = args
    for (const seg of f.path.slice(0, -1)) {
      if (!isRecord(target[seg])) target[seg] = {}
      target = target[seg] as Record<string, unknown>
    }
    target[f.path[f.path.length - 1]] = value
  }
  return { args, errors }
}
