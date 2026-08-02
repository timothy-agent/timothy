import type {
  AdminAgent,
  AdminConnector,
  AdminProvider,
  AdminRoute,
  AdminTool,
  AvailableModel,
  BudgetStatus,
  CacheRow,
  ChainEntry,
  ChatEvent,
  ChatRequest,
  EntityGraphData,
  GroupTotal,
  LatencyRow,
  MemoryItem,
  Mission,
  MissionEvent,
  MissionFile,
  MissionUsage,
  MissionTemplate,
  Notification,
  PendingPermission,
  ProviderHealth,
  RetrievedMemory,
  Schedule,
  SessionMeta,
  SessionUsage,
  TestResult,
  Transcript,
  UsagePoint,
  UsageSummary,
} from './types'

const tokenKey = 'timothy.token'

export function getToken(): string {
  return localStorage.getItem(tokenKey) ?? ''
}

export function setToken(token: string) {
  localStorage.setItem(tokenKey, token.trim())
}

// consumeTokenFragment reads a `#token=...` value from the URL fragment
// (as printed by the installer), stores it, and strips the fragment so
// it doesn't linger in the address bar or history. No-op if absent.
export function consumeTokenFragment() {
  const params = new URLSearchParams(window.location.hash.replace(/^#/, ''))
  const token = params.get('token')
  if (!token) return

  setToken(token)
  history.replaceState(null, '', window.location.pathname + window.location.search)
}

// createSSEParser incrementally parses an SSE byte stream that may be
// chunked at arbitrary boundaries. Each complete "data:" block is
// JSON-decoded and passed to onEvent; malformed blocks are skipped.
// Generic so events.ts's /v1/events stream (frames shaped differently
// than ChatEvent) can reuse the same parsing without a cast.
export function createSSEParser<T = ChatEvent>(onEvent: (ev: T) => void) {
  let buf = ''

  const emit = (block: string) => {
    const data = block
      .split('\n')
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice(5).trim())
      .join('\n')
    if (!data) return
    try {
      onEvent(JSON.parse(data) as T)
    } catch {
      // Malformed frame: skip rather than kill the stream.
    }
  }

  return {
    feed(text: string) {
      buf += text
      let idx = buf.indexOf('\n\n')
      while (idx >= 0) {
        emit(buf.slice(0, idx))
        buf = buf.slice(idx + 2)
        idx = buf.indexOf('\n\n')
      }
    },
    end() {
      if (buf.trim()) emit(buf)
      buf = ''
    },
  }
}

// ChatError is a structured request failure: match on status/code, not
// message text. sessionId is present when brain already created a
// session row — reuse it on retry instead of orphaning it.
export class ChatError extends Error {
  readonly status: number
  readonly code?: string
  readonly sessionId?: string

  constructor(status: number, message: string, code?: string, sessionId?: string) {
    super(message)
    this.name = 'ChatError'
    this.status = status
    this.code = code
    this.sessionId = sessionId
  }
}

export interface ChatStreamOptions {
  signal?: AbortSignal
  // Fired as soon as the response headers arrive: the session id is
  // known before the first event, so a mid-stream cut cannot lose it.
  onSession?: (id: string) => void
}

// chatStream posts one turn and delivers every SSE event, ending with
// the terminal meta event. Throws ChatError on non-200 responses.
// With a session_id it targets that session's messages endpoint;
// without one it uses /v1/chat, which creates the session.
// The /v1 path is same-origin by design: the dev server proxies it to
// brain, and production serves the SPA behind the same reverse proxy
// as the API.
export async function chatStream(
  req: ChatRequest,
  onEvent: (ev: ChatEvent) => void,
  opts: ChatStreamOptions = {},
): Promise<void> {
  const { session_id, ...body } = req
  const url = session_id ? `/v1/sessions/${session_id}/messages` : '/v1/chat'
  return postSSE(url, session_id ? body : req, onEvent, opts)
}

// retryStream re-runs a session's last (failed) turn: the session
// already carries the dangling user message server-side, so this
// posts no body — retry has nothing new to say, just "try again".
export async function retryStream(
  sessionId: string,
  onEvent: (ev: ChatEvent) => void,
  opts: ChatStreamOptions = {},
): Promise<void> {
  return postSSE(`/v1/sessions/${sessionId}/messages/retry`, undefined, onEvent, opts)
}

// stopTurn cancels a session's in-flight turn server-side: the turn now
// runs detached from the request that started it, so aborting the
// local fetch (AbortController) no longer stops it — this is the only
// thing that does. A 404 (no turn running, or it already finished) is
// a benign race from the caller's point of view, same as streamLive's.
export async function stopTurn(sessionId: string): Promise<void> {
  await request<void>(`/v1/sessions/${sessionId}/stop`, { method: 'POST' })
}

// streamLive reattaches to a session's in-flight turn (Tier 2 of live
// reattach): GET .../live replays whatever the turn already emitted
// then follows it live until the terminal, wire-identical to
// chatStream/retryStream's SSE frames — same createSSEParser, same
// ChatEvent shape, same terminal meta contract — so a caller feeds
// events through the exact same applyEvent reducer regardless of
// which of the three started the stream. Throws ChatError(404,
// 'no_active_turn') when nothing is running; the caller's fallback is
// a plain transcript refetch, not a retry loop.
export async function streamLive(
  sessionId: string,
  onEvent: (ev: ChatEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(`/v1/sessions/${sessionId}/live`, {
    headers: { Authorization: `Bearer ${getToken()}` },
    signal,
  })
  if (!res.ok || !res.body) {
    const text = await res.text().catch(() => '')
    let code: string | undefined
    let message = text
    try {
      const parsed = JSON.parse(text) as { error?: string; message?: string }
      code = parsed.error
      message = parsed.message ?? text
    } catch {
      // Non-JSON error body: keep the raw text.
    }
    throw new ChatError(res.status, message || `live stream failed (${res.status})`, code)
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  const parser = createSSEParser(onEvent)
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    parser.feed(decoder.decode(value, { stream: true }))
  }
  parser.end()
}

// postSSE is chatStream and retryStream's shared body: POST, surface a
// structured ChatError on failure, then relay the SSE stream until the
// terminal meta event.
async function postSSE(
  url: string,
  body: unknown,
  onEvent: (ev: ChatEvent) => void,
  { signal, onSession }: ChatStreamOptions,
): Promise<void> {
  const res = await fetch(url, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${getToken()}`,
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
    signal,
  })
  if (!res.ok || !res.body) {
    const text = await res.text().catch(() => '')
    let code: string | undefined
    let message = text
    let sessionId: string | undefined
    try {
      const parsed = JSON.parse(text) as { error?: string; message?: string; session_id?: string }
      code = parsed.error
      message = parsed.message ?? text
      sessionId = parsed.session_id || undefined
    } catch {
      // Non-JSON error body: keep the raw text.
    }
    throw new ChatError(res.status, message || `chat failed (${res.status})`, code, sessionId)
  }

  const headerSession = res.headers.get('X-Session-Id')
  if (headerSession) onSession?.(headerSession)

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  const parser = createSSEParser(onEvent)
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    parser.feed(decoder.decode(value, { stream: true }))
  }
  parser.end()
}

// request is the plain-JSON counterpart of chatStream: same auth, same
// structured errors.
async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${getToken()}`,
      ...init.headers,
    },
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    let code: string | undefined
    let message = body
    try {
      const parsed = JSON.parse(body) as { error?: string; message?: string }
      code = parsed.error
      message = parsed.message ?? body
    } catch {
      // Non-JSON error body: keep the raw text.
    }
    throw new ChatError(res.status, message || `request failed (${res.status})`, code)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

// SessionCursor is the last row of the previous page: both halves
// travel together so ties on updated_at cannot drop or repeat rows.
export interface SessionCursor {
  before: string
  beforeId: string
}

// listSessions returns one page (newest first). Pass the previous
// page's last row as the cursor to fetch the next page.
export async function listSessions(query = '', cursor?: SessionCursor): Promise<SessionMeta[]> {
  const params = new URLSearchParams()
  if (query) params.set('query', query)
  if (cursor) {
    params.set('before', cursor.before)
    params.set('before_id', cursor.beforeId)
  }
  const qs = params.size > 0 ? `?${params.toString()}` : ''
  const { sessions } = await request<{ sessions: SessionMeta[] }>(`/v1/sessions${qs}`)
  return sessions
}

export async function getTranscript(id: string): Promise<Transcript> {
  return request<Transcript>(`/v1/sessions/${id}`)
}

// answerPermission resolves a parked tool call.
export async function answerPermission(
  id: string,
  decision: 'once' | 'session' | 'deny',
): Promise<void> {
  await request<void>(`/v1/permissions/${id}`, {
    method: 'POST',
    body: JSON.stringify({ decision }),
  })
}

// listPendingPermissions returns every permission ask still awaiting a
// decision across every session with a currently active turn — the
// global badge/toast's data source.
export async function listPendingPermissions(): Promise<PendingPermission[]> {
  const { pending } = await request<{ pending: PendingPermission[] }>('/v1/permissions/pending')
  return pending ?? []
}

export async function updateSession(
  id: string,
  patch: { title?: string; archived?: boolean },
): Promise<void> {
  await request<void>(`/v1/sessions/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

export async function deleteSession(id: string): Promise<void> {
  await request<void>(`/v1/sessions/${id}`, { method: 'DELETE' })
}

// transcribe posts a recorded audio clip (from the mic button) to
// brain's local speech-to-text proxy and returns the transcript.
// Raw bytes, not JSON — the body IS the audio, so this bypasses
// request()'s JSON content type.
export async function transcribe(blob: Blob): Promise<string> {
  const res = await fetch('/v1/transcribe', {
    method: 'POST',
    headers: { Authorization: `Bearer ${getToken()}` },
    body: blob,
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    let message = body
    try {
      const parsed = JSON.parse(body) as { message?: string }
      message = parsed.message ?? body
    } catch {
      // Non-JSON error body: keep the raw text.
    }
    throw new ChatError(res.status, message || `transcribe failed (${res.status})`)
  }
  const { text } = (await res.json()) as { text: string }
  return text
}

// AttachmentUpload is the store's view of a saved attachment (PR
// #120): id is the content hash, used as both the transcript
// reference and the /v1/attachments/{id} download path.
export interface AttachmentUpload {
  id: string
  mime: string
  size_bytes: number
}

// uploadAttachment posts one file to /v1/attachments (multipart field
// "file") and returns its stored id/mime/size. Raw multipart, not
// JSON — same bypass of request()'s JSON content type as transcribe.
export async function uploadAttachment(file: File): Promise<AttachmentUpload> {
  const form = new FormData()
  form.append('file', file)
  const res = await fetch('/v1/attachments', {
    method: 'POST',
    headers: { Authorization: `Bearer ${getToken()}` },
    body: form,
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    let message = body
    try {
      const parsed = JSON.parse(body) as { message?: string }
      message = parsed.message ?? body
    } catch {
      // Non-JSON error body: keep the raw text.
    }
    throw new ChatError(res.status, message || `upload failed (${res.status})`)
  }
  return (await res.json()) as AttachmentUpload
}

// fetchAttachmentBlob reads an uploaded attachment's bytes for inline
// rendering (AuthedImage) — GET /v1/attachments/{id} requires the
// bearer header, so a bare <img src> cannot fetch it directly.
export async function fetchAttachmentBlob(id: string): Promise<Blob> {
  const res = await fetch(`/v1/attachments/${id}`, {
    headers: { Authorization: `Bearer ${getToken()}` },
  })
  if (!res.ok) {
    throw new ChatError(res.status, `request failed (${res.status})`)
  }
  return res.blob()
}

// --- Long-term memory (queue + browser) ---

export async function listMemories(
  status: MemoryItem['status'],
  types?: string[],
): Promise<MemoryItem[]> {
  const params = new URLSearchParams({ status })
  if (types && types.length > 0) params.set('types', types.join(','))
  const { memories } = await request<{ memories: MemoryItem[] }>(`/v1/memories?${params}`)
  return memories ?? []
}

export async function addMemory(content: string, type = 'semantic'): Promise<string> {
  const { id } = await request<{ id: string }>('/v1/memories', {
    method: 'POST',
    body: JSON.stringify({ content, type }),
  })
  return id
}

// resolveMemory answers a queue card. Pass content to edit-then-confirm.
export async function resolveMemory(
  id: string,
  action: 'confirm' | 'reject',
  content?: string,
): Promise<void> {
  await request<void>(`/v1/memories/${id}`, {
    method: 'POST',
    body: JSON.stringify(content ? { action, content } : { action }),
  })
}

export async function memoryChain(id: string): Promise<MemoryItem[]> {
  const { chain } = await request<{ chain: MemoryItem[] }>(`/v1/memories/${id}/chain`)
  return chain ?? []
}

export async function searchMemories(query: string): Promise<RetrievedMemory[]> {
  const { memories } = await request<{ memories: RetrievedMemory[] }>('/v1/memories/search', {
    method: 'POST',
    body: JSON.stringify({ query }),
  })
  return memories ?? []
}

export async function entityGraph(): Promise<EntityGraphData> {
  const data = await request<EntityGraphData>('/v1/entities/graph')
  return { entities: data.entities ?? [], edges: data.edges ?? [] }
}

export async function entityMemories(id: string): Promise<MemoryItem[]> {
  const { memories } = await request<{ memories: MemoryItem[] }>(`/v1/entities/${id}/memories`)
  return memories ?? []
}

// --- admin usage (dashboard) ---

function rangeParams(from: Date, to: Date, extra: Record<string, string> = {}): string {
  const params = new URLSearchParams({
    from: from.toISOString(),
    to: to.toISOString(),
    ...extra,
  })
  return params.toString()
}

export async function usageSummary(from: Date, to: Date): Promise<UsageSummary> {
  return request<UsageSummary>(`/v1/admin/usage/summary?${rangeParams(from, to)}`)
}

export async function usageSeries(
  from: Date,
  to: Date,
  bucket: 'hour' | 'day' | 'week',
  group: 'provider' | 'model' | 'route',
): Promise<UsagePoint[]> {
  const { points } = await request<{ points: UsagePoint[] }>(
    `/v1/admin/usage/series?${rangeParams(from, to, { bucket, group })}`,
  )
  return points ?? []
}

export async function usageTotals(
  from: Date,
  to: Date,
  group: 'provider' | 'model' | 'route',
): Promise<GroupTotal[]> {
  const { totals } = await request<{ totals: GroupTotal[] }>(
    `/v1/admin/usage/totals?${rangeParams(from, to, { group })}`,
  )
  return totals ?? []
}

export async function usageSessions(from: Date, to: Date, limit = 10): Promise<SessionUsage[]> {
  const { sessions } = await request<{ sessions: SessionUsage[] }>(
    `/v1/admin/usage/sessions?${rangeParams(from, to, { limit: String(limit) })}`,
  )
  return sessions ?? []
}

export async function usageLatency(from: Date, to: Date): Promise<LatencyRow[]> {
  const { providers } = await request<{ providers: LatencyRow[] }>(
    `/v1/admin/usage/latency?${rangeParams(from, to)}`,
  )
  return providers ?? []
}

export async function usageCache(from: Date, to: Date): Promise<CacheRow[]> {
  const { providers } = await request<{ providers: CacheRow[] }>(
    `/v1/admin/usage/cache?${rangeParams(from, to)}`,
  )
  return providers ?? []
}

export async function usageBudget(): Promise<BudgetStatus> {
  return request<BudgetStatus>('/v1/admin/usage/budget')
}

// patchBudget updates spend limits per window: a number sets, null
// clears, an absent key stays untouched.
export async function patchBudget(changes: {
  day?: number | null
  month?: number | null
}): Promise<void> {
  await request<void>('/v1/admin/usage/budget', {
    method: 'PATCH',
    body: JSON.stringify(changes),
  })
}

// --- admin control plane (settings panel) ---

export async function listProviders(): Promise<AdminProvider[]> {
  const { providers } = await request<{ providers: AdminProvider[] }>('/v1/admin/providers')
  // Rows written before the backend guarded typed nils can carry
  // models/headers as JSON null; a null models array would crash every
  // component that maps over it, blanking the whole settings page.
  return (providers ?? []).map((p) => ({ ...p, models: p.models ?? [], headers: p.headers ?? {} }))
}

export async function createProvider(p: Partial<AdminProvider>): Promise<string> {
  const { id } = await request<{ id: string }>('/v1/admin/providers', {
    method: 'POST',
    body: JSON.stringify(p),
  })
  return id
}

export async function patchProvider(id: string, patch: Partial<AdminProvider>): Promise<void> {
  await request<void>(`/v1/admin/providers/${id}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  })
}

export async function deleteProvider(id: string): Promise<void> {
  await request<void>(`/v1/admin/providers/${id}`, { method: 'DELETE' })
}

export async function testProvider(id: string, model?: string): Promise<TestResult> {
  return request<TestResult>(`/v1/admin/providers/${id}/test`, {
    method: 'POST',
    body: JSON.stringify(model ? { model } : {}),
  })
}

// validateProvider probes an UNSAVED provider config with a one-token
// completion — the add dialog's validate-on-create. Probe failures come
// back as { ok: false, detail }; only invalid configs throw.
export async function validateProvider(
  p: Partial<AdminProvider>,
  model: string,
): Promise<TestResult> {
  return request<TestResult>('/v1/admin/providers/validate', {
    method: 'POST',
    body: JSON.stringify({ ...p, model }),
  })
}

// availableModels proxies the provider's own model-listing endpoint.
// Throws ChatError with status 422 when the driver cannot list models
// (bedrock) — callers fall back to manual entry.
export async function availableModels(id: string): Promise<AvailableModel[]> {
  const { models } = await request<{ models: AvailableModel[] }>(
    `/v1/admin/providers/${id}/models`,
  )
  return models ?? []
}

export async function providersHealth(): Promise<ProviderHealth[]> {
  const { providers } = await request<{ providers: ProviderHealth[] }>(
    '/v1/admin/providers/health',
  )
  return providers ?? []
}

// setSecret stores a credential under refName through the store-wide
// default backend (write-only: it is never returned by any endpoint).
// Built-in storage encrypts the value; a Vault/ASM default records it
// as the reference of a secret already held there. deleteSecret
// removes it; the provider then builds without a key and shows
// unhealthy until a new value is set.
export async function setSecret(refName: string, value: string): Promise<void> {
  await request<void>(`/v1/admin/secrets/${encodeURIComponent(refName)}`, {
    method: 'PUT',
    body: JSON.stringify({ value }),
  })
}

// setSecretStorage pins built-in storage regardless of the default
// backend. Only for backend bootstrap credentials (the vault token,
// ASM secret key): the credential that unlocks an external backend
// cannot live behind that backend.
export async function setSecretStorage(refName: string, value: string): Promise<void> {
  await request<void>(`/v1/admin/secrets/${encodeURIComponent(refName)}`, {
    method: 'PUT',
    body: JSON.stringify({ value, backend: 'db' }),
  })
}

export async function deleteSecret(refName: string): Promise<void> {
  await request<void>(`/v1/admin/secrets/${encodeURIComponent(refName)}`, { method: 'DELETE' })
}

export interface SecretStatus {
  configured: boolean
  backend: string
}

export async function secretStatus(refName: string): Promise<SecretStatus> {
  if (!refName) return { configured: false, backend: '' }
  return request<SecretStatus>(`/v1/admin/secrets/${encodeURIComponent(refName)}`)
}

export interface SecretBackendStatus {
  backend: string
  configured: boolean
  default: boolean
}

// listSecretBackends reports each backend's configured/default state;
// exactly one is the default all credential writes route through.
export async function listSecretBackends(): Promise<SecretBackendStatus[]> {
  const { backends } = await request<{ backends: SecretBackendStatus[] }>(
    '/v1/admin/secret-backends',
  )
  return backends ?? []
}

export async function setDefaultSecretBackend(backend: string): Promise<void> {
  await request<void>('/v1/admin/secret-backends/default', {
    method: 'PUT',
    body: JSON.stringify({ backend }),
  })
}

export async function getSecretBackendConfig(
  backend: 'vault' | 'asm' | 'file',
): Promise<Record<string, string>> {
  const { config } = await request<{ config: Record<string, string> }>(
    `/v1/admin/secret-backends/${backend}`,
  )
  return config ?? {}
}

export async function putSecretBackendConfig(
  backend: 'vault' | 'asm' | 'file',
  config: Record<string, string>,
): Promise<void> {
  await request<void>(`/v1/admin/secret-backends/${backend}`, {
    method: 'PUT',
    body: JSON.stringify({ config }),
  })
}

export async function deleteSecretBackendConfig(backend: 'vault' | 'asm' | 'file'): Promise<void> {
  await request<void>(`/v1/admin/secret-backends/${backend}`, { method: 'DELETE' })
}

export async function testSecretBackend(
  backend: 'vault' | 'asm' | 'file',
): Promise<{ ok: boolean; error?: string }> {
  return request<{ ok: boolean; error?: string }>(`/v1/admin/secret-backends/${backend}/test`, {
    method: 'POST',
  })
}

// --- agents (who serves a session) ---

export async function listAgents(): Promise<AdminAgent[]> {
  const { agents } = await request<{ agents: AdminAgent[] }>('/v1/admin/agents')
  return agents ?? []
}

// listTools lists the live tool surface (builtins + connector tools),
// feeding the agent editor's tools allowlist picker.
export async function listTools(): Promise<AdminTool[]> {
  const { tools } = await request<{ tools: AdminTool[] }>('/v1/admin/tools')
  return tools ?? []
}

export async function createAgent(a: Partial<AdminAgent>): Promise<string> {
  const { id } = await request<{ id: string }>('/v1/admin/agents', {
    method: 'POST',
    body: JSON.stringify(a),
  })
  return id
}

export async function patchAgent(id: string, patch: Partial<AdminAgent>): Promise<void> {
  await request<void>(`/v1/admin/agents/${id}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  })
}

export async function setDefaultAgent(id: string): Promise<void> {
  await request<void>(`/v1/admin/agents/${id}/default`, { method: 'PUT' })
}

export async function deleteAgent(id: string): Promise<void> {
  await request<void>(`/v1/admin/agents/${id}`, { method: 'DELETE' })
}

// --- connectors (integrations) ---

export async function listConnectors(): Promise<AdminConnector[]> {
  const { connectors } = await request<{ connectors: AdminConnector[] }>('/v1/admin/connectors')
  return connectors ?? []
}

export async function createConnector(c: Partial<AdminConnector>): Promise<string> {
  const { id } = await request<{ id: string }>('/v1/admin/connectors', {
    method: 'POST',
    body: JSON.stringify(c),
  })
  return id
}

export async function patchConnector(
  id: string,
  patch: Partial<Pick<AdminConnector, 'config' | 'credential_ref' | 'enabled' | 'sensitive'>>,
): Promise<void> {
  await request<void>(`/v1/admin/connectors/${id}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  })
}

export async function deleteConnector(id: string): Promise<void> {
  await request<void>(`/v1/admin/connectors/${id}`, { method: 'DELETE' })
}

export async function testConnector(id: string): Promise<{ ok: boolean; error?: string }> {
  return request<{ ok: boolean; error?: string }>(`/v1/admin/connectors/${id}/test`, {
    method: 'POST',
  })
}

// connectorOAuthStart returns the Google consent URL to send the
// browser to; the callback lands back on /settings?tab=connectors.
export async function connectorOAuthStart(id: string): Promise<string> {
  const { url } = await request<{ url: string }>(`/v1/admin/connectors/${id}/oauth/start`, {
    method: 'POST',
  })
  return url
}

export async function listRoutes(): Promise<AdminRoute[]> {
  const { routes } = await request<{ routes: AdminRoute[] }>('/v1/admin/routes')
  return routes ?? []
}

export async function patchRoute(
  name: string,
  patch: { chain?: ChainEntry[]; strategy?: string; enabled?: boolean },
): Promise<void> {
  await request<void>(`/v1/admin/routes/${name}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  })
}

export async function createRoute(name: string, capability: string): Promise<string> {
  const { id } = await request<{ id: string }>('/v1/admin/routes', {
    method: 'POST',
    body: JSON.stringify({ name, capability }),
  })
  return id
}

export async function deleteRoute(name: string): Promise<void> {
  await request<void>(`/v1/admin/routes/${name}`, { method: 'DELETE' })
}

export async function setRouteRole(name: string, role: string): Promise<void> {
  await request<void>(`/v1/admin/routes/${name}/role`, {
    method: 'PUT',
    body: JSON.stringify({ role }),
  })
}

export interface AdminSettings {
  settings: Record<string, boolean>
  // Typed runtime settings; empty string means the built-in default.
  values: Record<string, string>
}

export async function getSettings(): Promise<AdminSettings> {
  const s = await request<AdminSettings>('/v1/admin/settings')
  return { settings: s.settings ?? {}, values: s.values ?? {} }
}

export async function patchSettings(changes: Record<string, boolean>): Promise<void> {
  await request<void>('/v1/admin/settings', {
    method: 'PATCH',
    body: JSON.stringify(changes),
  })
}

// patchSettingValues updates typed runtime settings (strings; empty
// resets a key to its built-in default).
export async function patchSettingValues(changes: Record<string, string>): Promise<void> {
  await request<void>('/v1/admin/settings/values', {
    method: 'PATCH',
    body: JSON.stringify(changes),
  })
}

// --- missions (long-running, agent-driven units of work) ---

export interface CreateMissionInput {
  goal: string
  kind: 'coding' | 'research'
  agent_id?: string
  route?: string
  review_route?: string
  escalation_route?: string
  max_iterations?: number
  budget_usd?: number
  repo_path?: string
  auto_approve_safe?: boolean
}

// listMissions returns every mission by default; opts narrows to one
// schedule's fire history (scheduleId) and/or caps the result count
// (limit) — both map directly to the server's optional query params.
export async function listMissions(opts?: {
  scheduleId?: string
  limit?: number
}): Promise<Mission[]> {
  const params = new URLSearchParams()
  if (opts?.scheduleId) params.set('schedule_id', opts.scheduleId)
  if (opts?.limit) params.set('limit', String(opts.limit))
  const qs = params.size > 0 ? `?${params.toString()}` : ''
  const { missions } = await request<{ missions: Mission[] }>(`/v1/missions${qs}`)
  return missions ?? []
}

export async function createMission(input: CreateMissionInput): Promise<{ id: string }> {
  return request<{ id: string }>('/v1/missions', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export async function getMission(id: string): Promise<Mission> {
  return request<Mission>(`/v1/missions/${id}`)
}

export async function missionEvents(id: string): Promise<MissionEvent[]> {
  const { events } = await request<{ events: MissionEvent[] }>(`/v1/missions/${id}/events`)
  return events ?? []
}

export async function missionUsage(id: string): Promise<MissionUsage> {
  return request<MissionUsage>(`/v1/admin/usage/mission?id=${encodeURIComponent(id)}`)
}

export async function resumeMission(id: string): Promise<void> {
  await request<void>(`/v1/missions/${id}/resume`, { method: 'POST' })
}

export async function cancelMission(id: string): Promise<void> {
  await request<void>(`/v1/missions/${id}/cancel`, { method: 'POST' })
}

export async function deleteMission(id: string): Promise<void> {
  await request<void>(`/v1/missions/${id}`, { method: 'DELETE' })
}

export async function answerMissionPermission(
  id: string,
  decision: 'once' | 'session' | 'deny',
): Promise<void> {
  await request<void>(`/v1/missions/${id}/permission`, {
    method: 'POST',
    body: JSON.stringify({ decision }),
  })
}

export async function listNotifications(): Promise<Notification[]> {
  const { notifications } = await request<{ notifications: Notification[] }>('/v1/notifications')
  return notifications ?? []
}

export async function markNotificationRead(id: string): Promise<void> {
  await request<void>(`/v1/notifications/${id}/read`, { method: 'POST' })
}

// --- mission artifacts + push ---

export async function listMissionFiles(
  id: string,
): Promise<{ files: MissionFile[]; truncated: boolean }> {
  const r = await request<{ files: MissionFile[]; truncated: boolean }>(
    `/v1/missions/${id}/files`,
  )
  return { files: r.files ?? [], truncated: r.truncated ?? false }
}

// fetchBlobDownload fetches an authenticated binary response and saves
// it via a programmatic anchor click — plain hrefs can't carry the
// bearer token from localStorage.
async function fetchBlobDownload(path: string, fallbackName: string): Promise<void> {
  const res = await fetch(path, {
    headers: { Authorization: `Bearer ${getToken()}` },
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    let code: string | undefined
    let message = body
    try {
      const parsed = JSON.parse(body) as { error?: string; message?: string }
      code = parsed.error
      message = parsed.message ?? body
    } catch {
      // Non-JSON error body: keep the raw text.
    }
    throw new ChatError(res.status, message || `request failed (${res.status})`, code)
  }
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = fallbackName
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

function encodeFilePath(path: string): string {
  return path.split('/').map(encodeURIComponent).join('/')
}

// downloadMissionFile encodes each path segment individually so
// filenames containing '/' cannot be mistaken for it (the server
// mirrors this per-segment scheme on GET /v1/missions/:id/files/*).
export async function downloadMissionFile(id: string, path: string): Promise<void> {
  const fallbackName = path.split('/').pop() || path
  return fetchBlobDownload(`/v1/missions/${id}/files/${encodeFilePath(path)}`, fallbackName)
}

// missionFilePreviewCap bounds in-app preview: the server always
// serves the full file (download is uncapped), but rendering a huge
// file inline would hang the tab, so previews over this size fall
// back to "download instead" in the UI.
export const missionFilePreviewCap = 1_000_000

export class MissionFileTooLargeError extends Error {}

// fetchMissionFileBlob reads a mission file's bytes for in-app
// preview (image/text/markdown) rather than triggering a save — the
// server forces Content-Type: application/octet-stream on this route
// deliberately (a worker-authored file could be arbitrary HTML), so
// callers must render the bytes themselves, never navigate to the URL.
export async function fetchMissionFileBlob(id: string, path: string): Promise<Blob> {
  const res = await fetch(`/v1/missions/${id}/files/${encodeFilePath(path)}`, {
    headers: { Authorization: `Bearer ${getToken()}` },
  })
  if (!res.ok) {
    throw new ChatError(res.status, `request failed (${res.status})`)
  }
  const len = res.headers.get('Content-Length')
  if (len && Number(len) > missionFilePreviewCap) {
    throw new MissionFileTooLargeError('file too large to preview')
  }
  const blob = await res.blob()
  if (blob.size > missionFilePreviewCap) {
    throw new MissionFileTooLargeError('file too large to preview')
  }
  return blob
}

export async function downloadMissionArchive(id: string): Promise<void> {
  return fetchBlobDownload(`/v1/missions/${id}/archive`, `mission-${id.slice(0, 8)}.zip`)
}

export async function pushMission(
  id: string,
  credentialRef: string,
): Promise<{ branch: string; remote_host: string }> {
  return request<{ branch: string; remote_host: string }>(`/v1/missions/${id}/push`, {
    method: 'POST',
    body: JSON.stringify({ credential_ref: credentialRef }),
  })
}

// --- schedules (recurring cron triggers that fire mission templates) ---

export interface CreateScheduleInput {
  name: string
  cron: string
  mission_template: MissionTemplate
  enabled?: boolean
  expires_at?: string
}

export async function listSchedules(): Promise<Schedule[]> {
  const { schedules } = await request<{ schedules: Schedule[] }>('/v1/schedules')
  return schedules ?? []
}

export async function createSchedule(input: CreateScheduleInput): Promise<{ id: string }> {
  return request<{ id: string }>('/v1/schedules', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

export async function patchSchedule(
  id: string,
  patch: {
    name?: string
    cron?: string
    mission_template?: MissionTemplate
    enabled?: boolean
    expires_at?: string | null
  },
): Promise<Schedule> {
  return request<Schedule>(`/v1/schedules/${id}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  })
}

export async function deleteSchedule(id: string): Promise<void> {
  await request<void>(`/v1/schedules/${id}`, { method: 'DELETE' })
}
