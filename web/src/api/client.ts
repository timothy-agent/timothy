import type {
  AdminProvider,
  AdminRoute,
  BudgetStatus,
  CacheRow,
  ChainEntry,
  ChatEvent,
  ChatRequest,
  LatencyRow,
  MemoryItem,
  ProviderHealth,
  RetrievedMemory,
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

// createSSEParser incrementally parses an SSE byte stream that may be
// chunked at arbitrary boundaries. Each complete "data:" block is
// JSON-decoded and passed to onEvent; malformed blocks are skipped.
export function createSSEParser(onEvent: (ev: ChatEvent) => void) {
  let buf = ''

  const emit = (block: string) => {
    const data = block
      .split('\n')
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice(5).trim())
      .join('\n')
    if (!data) return
    try {
      onEvent(JSON.parse(data) as ChatEvent)
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
  { signal, onSession }: ChatStreamOptions = {},
): Promise<void> {
  const { session_id, ...body } = req
  const url = session_id ? `/v1/sessions/${session_id}/messages` : '/v1/chat'
  const res = await fetch(url, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${getToken()}`,
    },
    body: JSON.stringify(session_id ? body : req),
    signal,
  })
  if (!res.ok || !res.body) {
    const body = await res.text().catch(() => '')
    let code: string | undefined
    let message = body
    let sessionId: string | undefined
    try {
      const parsed = JSON.parse(body) as { error?: string; message?: string; session_id?: string }
      code = parsed.error
      message = parsed.message ?? body
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

export async function updateSession(
  id: string,
  patch: { title?: string; archived?: boolean },
): Promise<void> {
  await request<void>(`/v1/sessions/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
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
  group: 'provider' | 'model' | 'category',
): Promise<UsagePoint[]> {
  const { points } = await request<{ points: UsagePoint[] }>(
    `/v1/admin/usage/series?${rangeParams(from, to, { bucket, group })}`,
  )
  return points ?? []
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
  return providers ?? []
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

export async function providersHealth(): Promise<ProviderHealth[]> {
  const { providers } = await request<{ providers: ProviderHealth[] }>(
    '/v1/admin/providers/health',
  )
  return providers ?? []
}

// setSecret stores a credential value under refName (write-only: it is
// never returned by any endpoint). deleteSecret removes it; the
// provider then builds without a key and shows unhealthy until a new
// value is set.
export async function setSecret(refName: string, value: string): Promise<void> {
  await request<void>(`/v1/admin/secrets/${encodeURIComponent(refName)}`, {
    method: 'PUT',
    body: JSON.stringify({ value }),
  })
}

// setSecretExternal points refName at a secret held in Vault or AWS
// Secrets Manager instead of storing a value here.
export async function setSecretExternal(
  refName: string,
  backend: 'vault' | 'asm',
  backendRef: string,
): Promise<void> {
  await request<void>(`/v1/admin/secrets/${encodeURIComponent(refName)}`, {
    method: 'PUT',
    body: JSON.stringify({ backend, backend_ref: backendRef }),
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

export async function getSecretBackendConfig(
  backend: 'vault' | 'asm',
): Promise<Record<string, string>> {
  const { config } = await request<{ config: Record<string, string> }>(
    `/v1/admin/secret-backends/${backend}`,
  )
  return config ?? {}
}

export async function putSecretBackendConfig(
  backend: 'vault' | 'asm',
  config: Record<string, string>,
): Promise<void> {
  await request<void>(`/v1/admin/secret-backends/${backend}`, {
    method: 'PUT',
    body: JSON.stringify({ config }),
  })
}

export async function deleteSecretBackendConfig(backend: 'vault' | 'asm'): Promise<void> {
  await request<void>(`/v1/admin/secret-backends/${backend}`, { method: 'DELETE' })
}

export async function testSecretBackend(
  backend: 'vault' | 'asm',
): Promise<{ ok: boolean; error?: string }> {
  return request<{ ok: boolean; error?: string }>(`/v1/admin/secret-backends/${backend}/test`, {
    method: 'POST',
  })
}

export async function listRoutes(): Promise<AdminRoute[]> {
  const { routes } = await request<{ routes: AdminRoute[] }>('/v1/admin/routes')
  return routes ?? []
}

export async function patchRoute(
  category: string,
  patch: { chain?: ChainEntry[]; enabled?: boolean },
): Promise<void> {
  await request<void>(`/v1/admin/routes/${category}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  })
}

export async function getSettings(): Promise<Record<string, boolean>> {
  const { settings } = await request<{ settings: Record<string, boolean> }>('/v1/admin/settings')
  return settings ?? {}
}

export async function patchSettings(changes: Record<string, boolean>): Promise<void> {
  await request<void>('/v1/admin/settings', {
    method: 'PATCH',
    body: JSON.stringify(changes),
  })
}
