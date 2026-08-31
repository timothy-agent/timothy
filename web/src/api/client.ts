import type {
  AdminAgent,
  AdminConnector,
  AdminProvider,
  AdminRoute,
  AdminSkill,
  AdminTool,
  AvailableModel,
  BudgetLimit,
  BudgetStatus,
  CacheRow,
  CatalogModel,
  CatalogPrice,
  CatalogPriceQuery,
  CatalogSyncStatus,
  ChainEntry,
  ChatEvent,
  ChatRequest,
  Destination,
  EntityGraphData,
  ExecutionPlanPhase,
  GitHubIdentity,
  GitHubRepo,
  GroupTotal,
  KbCollection,
  KbDocument,
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
  ReferenceKind,
  RetrievedMemory,
  Schedule,
  SessionMeta,
  TestResult,
  Transcript,
  UnpricedGroup,
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
// session row: reuse it on retry instead of orphaning it.
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

// Timothy's own bearer (TIMOTHY_API_TOKEN), not an LLM provider key.
// 401 is a stale/wrong token; auth_not_configured is an unset one.
export function isTimothyAuthError(err: unknown): boolean {
  return err instanceof ChatError && (err.status === 401 || err.code === 'auth_not_configured')
}

export const timothyAuthErrorMessage =
  "Timothy's API token is missing or invalid. Paste TIMOTHY_API_TOKEN from deploy/.env — this is not an LLM provider key."

export function errorText(err: unknown): string {
  if (isTimothyAuthError(err)) return timothyAuthErrorMessage
  return err instanceof Error ? err.message : String(err)
}

// subscribeNeedToken lets the app shell reopen the token dialog on any
// 401, including pages that never received Chat's onNeedToken callback.
// A failure before App has subscribed is kept pending and replayed so
// a child effect that 401s on mount is not lost (React runs child
// effects before parent effects).
const needTokenListeners = new Set<() => void>()
let pendingNeedToken = false

export function subscribeNeedToken(listener: () => void): () => void {
  needTokenListeners.add(listener)
  if (pendingNeedToken) listener()
  return () => {
    needTokenListeners.delete(listener)
  }
}

export function acknowledgeNeedToken() {
  pendingNeedToken = false
}

function emitNeedToken() {
  pendingNeedToken = true
  for (const listener of needTokenListeners) listener()
}

function failChat(status: number, message: string, code?: string, sessionId?: string): never {
  if (status === 401 || code === 'auth_not_configured') emitNeedToken()
  throw new ChatError(status, message, code, sessionId)
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
// posts no body: retry has nothing new to say, just "try again".
export async function retryStream(
  sessionId: string,
  onEvent: (ev: ChatEvent) => void,
  opts: ChatStreamOptions = {},
): Promise<void> {
  return postSSE(`/v1/sessions/${sessionId}/messages/retry`, undefined, onEvent, opts)
}

// stopTurn cancels a session's in-flight turn server-side: the turn now
// runs detached from the request that started it, so aborting the
// local fetch (AbortController) no longer stops it: this is the only
// thing that does. A 404 (no turn running, or it already finished) is
// a benign race from the caller's point of view, same as streamLive's.
export async function stopTurn(sessionId: string): Promise<void> {
  await request<void>(`/v1/sessions/${sessionId}/stop`, { method: 'POST' })
}

// streamLive reattaches to a session's in-flight turn (Tier 2 of live
// reattach): GET .../live replays whatever the turn already emitted
// then follows it live until the terminal, wire-identical to
// chatStream/retryStream's SSE frames: same createSSEParser, same
// ChatEvent shape, same terminal meta contract: so a caller feeds
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
    failChat(res.status, message || `live stream failed (${res.status})`, code)
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
    failChat(res.status, message || `chat failed (${res.status})`, code, sessionId)
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
    failChat(res.status, message || `request failed (${res.status})`, code)
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
// decision across every session with a currently active turn: the
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

// setSessionKnowledge replaces a session's pinned kb collection list.
export async function setSessionKnowledge(id: string, collections: string[]): Promise<void> {
  await request<void>(`/v1/sessions/${id}/knowledge`, {
    method: 'PUT',
    body: JSON.stringify({ collections }),
  })
}

// transcribe posts a recorded audio clip (from the mic button) to
// brain's local speech-to-text proxy and returns the transcript.
// Raw bytes, not JSON: the body IS the audio, so this bypasses
// request()'s JSON content type. language is an optional ISO 639-1
// code (e.g. "bn"); omitted lets the sidecar auto-detect.
export async function transcribe(blob: Blob, language?: string): Promise<string> {
  const url = language ? `/v1/transcribe?language=${encodeURIComponent(language)}` : '/v1/transcribe'
  const res = await fetch(url, {
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
    failChat(res.status, message || `transcribe failed (${res.status})`)
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
// JSON: same bypass of request()'s JSON content type as transcribe.
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
    failChat(res.status, message || `upload failed (${res.status})`)
  }
  return (await res.json()) as AttachmentUpload
}

// fetchAttachmentBlob reads an uploaded attachment's bytes for inline
// rendering (AuthedImage): GET /v1/attachments/{id} requires the
// bearer header, so a bare <img src> cannot fetch it directly.
export async function fetchAttachmentBlob(id: string): Promise<Blob> {
  const res = await fetch(`/v1/attachments/${id}`, {
    headers: { Authorization: `Bearer ${getToken()}` },
  })
  if (!res.ok) {
    failChat(res.status, `request failed (${res.status})`)
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

// usageSummary returns one row per billing currency present in the
// range: never summed together (D-013's spend-side sibling: no FX
// conversion anywhere in this codebase).
export async function usageSummary(from: Date, to: Date): Promise<UsageSummary[]> {
  const { summaries } = await request<{ summaries: UsageSummary[] }>(
    `/v1/admin/usage/summary?${rangeParams(from, to)}`,
  )
  return summaries ?? []
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

// usageUnpriced returns the (provider, model) pairs with unpriced usage
// (cost NULL) in range: the pairs Analytics' catalog estimate needs to
// price, kept per-provider so catalogPrices resolves each pair against
// that provider's own catalog candidates only.
export async function usageUnpriced(from: Date, to: Date): Promise<UnpricedGroup[]> {
  const { groups } = await request<{ groups: UnpricedGroup[] }>(
    `/v1/admin/usage/unpriced?${rangeParams(from, to)}`,
  )
  return groups ?? []
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

// patchBudget updates spend limits per window: a BudgetLimit sets,
// null clears, an absent key stays untouched.
export async function patchBudget(changes: {
  day?: BudgetLimit | null
  month?: BudgetLimit | null
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
  // headers as JSON null; a null headers object would crash every
  // component that reads it, blanking the whole settings page.
  return (providers ?? []).map((p) => ({ ...p, headers: p.headers ?? {} }))
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
// completion: the add dialog's validate-on-create. Probe failures come
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
// (bedrock): callers fall back to manual entry.
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

// --- model catalog (LiteLLM-synced pricing/context reference, suggest-only) ---

export async function catalogStatus(): Promise<CatalogSyncStatus> {
  return request<CatalogSyncStatus>('/v1/admin/catalog/status')
}

export async function refreshCatalog(): Promise<CatalogSyncStatus> {
  return request<CatalogSyncStatus>('/v1/admin/catalog/refresh', { method: 'POST' })
}

export async function searchCatalog(
  q: string,
  provider = '',
  limit = 50,
): Promise<CatalogModel[]> {
  const params = new URLSearchParams({ q, provider, limit: String(limit) })
  const { models } = await request<{ models: CatalogModel[] }>(
    `/v1/admin/catalog/models?${params}`,
  )
  return models ?? []
}

// catalogModelsForProvider searches the synced catalog restricted to a
// provider's candidate litellm_provider(s) (derived server-side from
// its driver/base_url, or "anthropic" for a kind='cli' claude-cli row)
//: the model id picker's live suggestion source, and the provider
// detail page's read-only Models list. q filters case-insensitive
// substring on model_key; omitted fetches the provider's whole
// candidate pool. limit defaults to the server's normal cap (50);
// pass the server's max (200) for a page that wants the whole pool.
export async function catalogModelsForProvider(
  providerId: string,
  q = '',
  limit?: number,
): Promise<CatalogModel[]> {
  const params = new URLSearchParams({ ...(q ? { q } : {}), ...(limit ? { limit: String(limit) } : {}) })
  const qs = params.size > 0 ? `?${params}` : ''
  const { models } = await request<{ models: CatalogModel[] }>(
    `/v1/admin/providers/${providerId}/catalog-models${qs}`,
  )
  return models ?? []
}

// catalogPrices resolves each (provider, model) pair within that
// PROVIDER's own catalog candidates only (Analytics' unpriced-call
// estimate): never the whole catalog, so a model name that collides
// with another vendor's catalog entry can never borrow that vendor's
// price. price is null when the provider name is unknown or the model
// has no match within its candidates.
export async function catalogPrices(pairs: CatalogPriceQuery[]): Promise<CatalogPrice[]> {
  if (pairs.length === 0) return []
  const { prices } = await request<{ prices: CatalogPrice[] }>('/v1/admin/catalog/prices', {
    method: 'POST',
    body: JSON.stringify(pairs),
  })
  return prices ?? []
}

// setSecret writes a raw credential value under refName through the
// store-wide default backend (write-only: it is never returned by any
// endpoint). Built-in storage encrypts it in Timothy's database; a
// Vault/ASM default has Timothy write it into that backend under the
// name timothy/refName. deleteSecret removes it; the provider then
// builds without a key and shows unhealthy until a new value is set.
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

// migrateSecret moves one stored ref's value onto backend, wiping its
// old storage: used to re-home a credential after Vault/ASM is set up.
export async function migrateSecret(refName: string, backend: string): Promise<void> {
  await request<void>(`/v1/admin/secrets/${encodeURIComponent(refName)}/migrate`, {
    method: 'POST',
    body: JSON.stringify({ backend }),
  })
}

export interface SecretMigrationResult {
  name: string
  migrated: boolean
  skipped: boolean
  error?: string
}

// migrateAllSecrets moves every stored ref not already on backend
// there; a per-ref failure lands in its result entry, never aborts the
// rest of the batch.
export async function migrateAllSecrets(backend: string): Promise<SecretMigrationResult[]> {
  const { results } = await request<{ results: SecretMigrationResult[] }>('/v1/admin/secrets/migrate', {
    method: 'POST',
    body: JSON.stringify({ backend }),
  })
  return results ?? []
}

// SecretReference is one provider or connector naming a credential ref
// as its credential_ref: the credentials panel's used-by chips.
export interface SecretReference {
  kind: 'provider' | 'connector'
  name: string
  role: 'credential' | 'oauth_tokens' | 'signing_key' | 'client_secret'
}

// SecretRefEntry is one stored secret's directory entry: name,
// timestamps (when the row has them), and every referent across both
// providers and connectors. Never a value: the credentials panel is a
// directory, not a vault viewer. system marks a configured secret
// backend's own bootstrap credential (e.g. the vault token): the
// gateway refuses to delete these regardless, but the panel hides the
// delete action for them up front.
export interface SecretRefEntry {
  name: string
  backend: string
  created_at?: string
  updated_at?: string
  referenced_by: SecretReference[]
  system?: boolean
}

// listSecretRefs lists every stored credential ref for the Credentials
// tab and the "use existing" pickers on provider/connector forms.
export async function listSecretRefs(): Promise<SecretRefEntry[]> {
  const { secrets } = await request<{ secrets: SecretRefEntry[] }>('/v1/admin/secrets')
  // referenced_by is normalized here so no consumer ever sees null.
  return (secrets ?? []).map((s) => ({ ...s, referenced_by: s.referenced_by ?? [] }))
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

// listSkills lists the loaded skill packs, feeding the agent editor's
// skills allowlist picker.
export async function listSkills(): Promise<AdminSkill[]> {
  const { skills } = await request<{ skills: AdminSkill[] }>('/v1/admin/skills')
  return skills ?? []
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

// --- knowledge (RAG collections agents search with search_kb) ---

export async function listKbCollections(): Promise<KbCollection[]> {
  const { collections } = await request<{ collections: KbCollection[] }>('/v1/admin/kb/collections')
  return collections ?? []
}

export async function getKbCollection(id: string): Promise<KbCollection> {
  return request<KbCollection>(`/v1/admin/kb/collections/${id}`)
}

export async function createKbCollection(c: { name: string; description: string }): Promise<string> {
  const { id } = await request<{ id: string }>('/v1/admin/kb/collections', {
    method: 'POST',
    body: JSON.stringify(c),
  })
  return id
}

export async function updateKbCollection(
  id: string,
  patch: { name?: string; description?: string; retrieval_weight?: number },
): Promise<KbCollection> {
  return request<KbCollection>(`/v1/admin/kb/collections/${id}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  })
}

export async function deleteKbCollection(id: string): Promise<void> {
  await request<void>(`/v1/admin/kb/collections/${id}`, { method: 'DELETE' })
}

export async function listKbDocuments(collectionId: string): Promise<KbDocument[]> {
  const { documents } = await request<{ documents: KbDocument[] }>(
    `/v1/admin/kb/collections/${collectionId}/documents`,
  )
  return documents ?? []
}

// searchKbDocuments is the cross-collection title search (the composer
// #-mention "type to find a kb document" search): an empty q returns
// every document.
export async function searchKbDocuments(q = ''): Promise<KbDocument[]> {
  const qs = q ? `?q=${encodeURIComponent(q)}` : ''
  const { documents } = await request<{ documents: KbDocument[] }>(`/v1/admin/kb/documents${qs}`)
  return documents ?? []
}

// uploadKbDocument posts one file to a collection (multipart field
// "file"): same bypass of request()'s JSON content type as
// uploadAttachment, since the body is the file, not JSON.
export async function uploadKbDocument(collectionId: string, file: File): Promise<KbDocument> {
  const form = new FormData()
  form.append('file', file)
  const res = await fetch(`/v1/admin/kb/collections/${collectionId}/documents`, {
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
    failChat(res.status, message || `upload failed (${res.status})`)
  }
  return (await res.json()) as KbDocument
}

export async function deleteKbDocument(id: string): Promise<void> {
  await request<void>(`/v1/admin/kb/documents/${id}`, { method: 'DELETE' })
}

// addKbDocumentFromUrl asks brain to fetch a public URL, convert it to
// markdown, and ingest it into the collection.
export async function addKbDocumentFromUrl(collectionId: string, url: string): Promise<KbDocument> {
  return request<KbDocument>(`/v1/admin/kb/collections/${collectionId}/documents/url`, {
    method: 'POST',
    body: JSON.stringify({ url }),
  })
}

export async function reingestKbDocument(id: string): Promise<void> {
  await request<void>(`/v1/admin/kb/documents/${id}/reingest`, { method: 'POST' })
}

// uploadKbDocumentAuto posts one file with no collection chosen: brain
// classifies it against existing collections (or creates a new one) and
// files it there. Same multipart shape as uploadKbDocument.
export async function uploadKbDocumentAuto(file: File): Promise<KbDocument> {
  const form = new FormData()
  form.append('file', file)
  const res = await fetch('/v1/admin/kb/documents', {
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
    failChat(res.status, message || `upload failed (${res.status})`)
  }
  return (await res.json()) as KbDocument
}

// addKbDocumentFromUrlAuto is addKbDocumentFromUrl with no collection
// chosen: brain classifies the fetched document the same way
// uploadKbDocumentAuto does.
export async function addKbDocumentFromUrlAuto(url: string): Promise<KbDocument> {
  return request<KbDocument>('/v1/admin/kb/documents/url', {
    method: 'POST',
    body: JSON.stringify({ url }),
  })
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
  patch: Partial<Pick<AdminConnector, 'name' | 'config' | 'credential_ref' | 'enabled' | 'sensitive'>>,
): Promise<void> {
  await request<void>(`/v1/admin/connectors/${id}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  })
}

export async function deleteConnector(id: string): Promise<void> {
  await request<void>(`/v1/admin/connectors/${id}`, { method: 'DELETE' })
}

export async function testConnector(
  id: string,
): Promise<{ ok: boolean; error?: string; identity?: GitHubIdentity }> {
  return request<{ ok: boolean; error?: string; identity?: GitHubIdentity }>(
    `/v1/admin/connectors/${id}/test`,
    { method: 'POST' },
  )
}

// listConnectorRepos lists every repo a github-kind connector's PAT
// can see (owner, collaborator, or org member), most recently pushed
// first: the mission create form's repo picker.
export async function listConnectorRepos(id: string): Promise<GitHubRepo[]> {
  const { repos } = await request<{ repos: GitHubRepo[] }>(`/v1/admin/connectors/${id}/repos`)
  return repos ?? []
}

// createConnectorRepo creates a new repo through a github-kind
// connector's PAT, auto-initialized so it has a default branch to
// clone into a mission workspace.
export async function createConnectorRepo(
  id: string,
  input: { name: string; private: boolean },
): Promise<GitHubRepo> {
  return request<GitHubRepo>(`/v1/admin/connectors/${id}/repos`, {
    method: 'POST',
    body: JSON.stringify(input),
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

// --- destinations (mission result delivery) ---

export async function listDestinations(): Promise<Destination[]> {
  const { destinations } = await request<{ destinations: Destination[] }>('/v1/admin/destinations')
  return destinations ?? []
}

export async function createDestination(d: Partial<Destination>): Promise<string> {
  const { id } = await request<{ id: string }>('/v1/admin/destinations', {
    method: 'POST',
    body: JSON.stringify(d),
  })
  return id
}

export async function patchDestination(
  id: string,
  patch: Partial<Pick<Destination, 'config' | 'credential_ref' | 'enabled'>>,
): Promise<void> {
  await request<void>(`/v1/admin/destinations/${id}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  })
}

export async function deleteDestination(id: string): Promise<void> {
  await request<void>(`/v1/admin/destinations/${id}`, { method: 'DELETE' })
}

// testDestination sends a canned "Timothy test delivery" payload
// through the destination's real adapter and reports success/failure.
export async function testDestination(id: string): Promise<{ ok: boolean; error?: string }> {
  return request<{ ok: boolean; error?: string }>(`/v1/admin/destinations/${id}/test`, {
    method: 'POST',
  })
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
  // Feature switches, plus the read-only derived transcribe_enabled
  // key (true when WHISPER_URL is configured server-side): the
  // Composer mic button hides itself when it's false.
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
  kind: 'coding' | 'general'
  agent_id?: string
  route?: string
  review_route?: string
  // plan_route, when set, is the route discover/plan/replan/prove run
  // on instead of route: omit (or "") to use route for everything.
  plan_route?: string
  escalation_route?: string
  // route_model/plan_route_model/review_route_model pin one phase axis
  // to one exact chain entry ("provider name/model") in the route it
  // would otherwise resolve: omit (or "") to keep the first-usable
  // walk. See ExecutionPlanEntry's provider_name+model for the pair a
  // pin names.
  route_model?: string
  plan_route_model?: string
  review_route_model?: string
  max_iterations?: number
  budget_amount?: number
  budget_currency?: string
  auto_approve_safe?: boolean
  // auto_approve_plan: omit (or true) advances straight from plan to
  // generate; false parks the mission awaiting operator approval once
  // the plan phase produces a plan.
  auto_approve_plan?: boolean
  // harness names the delegated coding-CLI executor a coding mission
  // runs under: omit (or "") to apply the settings default,
  // "native" to force the built-in agent loop. Only valid when
  // kind === 'coding'.
  harness?: string
  // environment selects the sandbox image key (D-05x) a coding
  // mission's container runs: omit (or "") to auto-detect (repo
  // markers, then a goal-keyword heuristic, falling back to base).
  // Only valid when kind === 'coding'.
  environment?: string
  // repo_url is a GitHub repo's https clone URL: when set, the mission
  // clones it instead of self-initializing an empty repo. Requires
  // connector_id (a github-kind connector's PAT authenticates the
  // clone) and is only valid when kind === 'coding'.
  repo_url?: string
  connector_id?: string
  // on_complete is the operator's consent-at-create choice for what the
  // harness does automatically once this mission reaches done: omit
  // (or "") does nothing, "push" pushes the branch, "push_pr" pushes
  // then opens a pull request. Requires repo_url + connector_id and
  // kind === 'coding'.
  on_complete?: '' | 'push' | 'push_pr'
  // branch_pattern/commit_style override the settings-configured git
  // strategy defaults for this mission alone; omit (or "") applies the
  // settings default. See FeaturesTab's git strategy cards for the
  // placeholder/style reference.
  branch_pattern?: string
  commit_style?: string
  // parent_mission_id, when set, makes this a follow-up mission: the
  // named mission must already be terminal (done/failed). Its branch,
  // when reachable, becomes this mission's worktree base, and its
  // outcome digest is carried into this mission's prompts.
  parent_mission_id?: string
  // attachments name already-uploaded PDFs (POST /v1/attachments) to
  // convert to markdown once at create time: PDF only, up to 8.
  attachments?: { id: string; name: string }[]
  // destination_ids names operator-created destinations to deliver this
  // mission's outcome digest to on the terminal done transition; omit
  // (or empty) delivers nowhere.
  destination_ids?: string[]
  // promote_kb_collection_id names a kb collection to promote this
  // mission's markdown artifacts into in the result phase's step
  // (D-081, issue #370; D-086); omit (or "") promotes nothing automatically.
  promote_kb_collection_id?: string
  // light requests a mission that skips discover/plan/prove (D-069):
  // single worker turn, final message delivered as the result. Only
  // valid when kind === 'general'.
  light?: boolean
  // references name composer #-mention picks (missions/chats/kb docs)
  // to resolve at create time into ReferencedContext.
  references?: { kind: ReferenceKind; id: string }[]
}

// ExecutorOption is one registered harness's usability on a given
// route (GET /v1/missions/executor-options): usable ones carry the
// provider/model they'd resolve to, unusable ones a reason why not.
export interface ExecutorOption {
  harness: string
  usable: boolean
  provider_name?: string
  model?: string
  reason?: string
}

export async function getMissionExecutorOptions(route?: string): Promise<ExecutorOption[]> {
  const qs = route ? `?route=${encodeURIComponent(route)}` : ''
  const { options } = await request<{ options: ExecutorOption[] }>(
    `/v1/missions/executor-options${qs}`,
  )
  return options ?? []
}

// getMissionExecutionPlan resolves all five mission phases (discover,
// plan, generate, prove, escalate) server-side for the given create
// inputs, so the frontend never mirrors route/harness precedence
// itself. Params match the create form's own fields; all optional.
export async function getMissionExecutionPlan(params: {
  kind?: string
  agent?: string
  harness?: string
  route?: string
  plan_route?: string
  review_route?: string
  escalation_route?: string
  route_model?: string
  plan_route_model?: string
  review_route_model?: string
  light?: boolean
}): Promise<ExecutionPlanPhase[]> {
  const qs = new URLSearchParams()
  if (params.kind) qs.set('kind', params.kind)
  if (params.agent) qs.set('agent', params.agent)
  if (params.harness) qs.set('harness', params.harness)
  if (params.route) qs.set('route', params.route)
  if (params.plan_route) qs.set('plan_route', params.plan_route)
  if (params.review_route) qs.set('review_route', params.review_route)
  if (params.escalation_route) qs.set('escalation_route', params.escalation_route)
  if (params.route_model) qs.set('route_model', params.route_model)
  if (params.plan_route_model) qs.set('plan_route_model', params.plan_route_model)
  if (params.review_route_model) qs.set('review_route_model', params.review_route_model)
  if (params.light) qs.set('light', 'true')
  const query = qs.toString()
  const { phases } = await request<{ phases: ExecutionPlanPhase[] }>(
    `/v1/missions/execution-plan${query ? `?${query}` : ''}`,
  )
  return phases ?? []
}

// listMissions returns every mission by default; opts narrows to one
// schedule's fire history (scheduleId), a text search (query, the
// composer #-mention mission search), and/or caps the result count
// (limit): all map directly to the server's optional query params.
export async function listMissions(opts?: {
  scheduleId?: string
  query?: string
  limit?: number
}): Promise<Mission[]> {
  const params = new URLSearchParams()
  if (opts?.scheduleId) params.set('schedule_id', opts.scheduleId)
  if (opts?.query) params.set('q', opts.query)
  if (opts?.limit) params.set('limit', String(opts.limit))
  const qs = params.size > 0 ? `?${params.toString()}` : ''
  const { missions } = await request<{ missions: Mission[] }>(`/v1/missions${qs}`)
  return missions ?? []
}

// createMission returns the full created mission (not just its id) so
// a server-resolved field decided at create time: e.g. auto-detected
// environment (D-05x): is available without a follow-up GET.
export async function createMission(input: CreateMissionInput): Promise<Mission> {
  return request<Mission>('/v1/missions', {
    method: 'POST',
    body: JSON.stringify(input),
  })
}

// classifyMission previews the kind create() would infer for goal, and
// (for a general goal) whether it looks like a single-pass light
// mission: the same classifyKind/classifyLight logic the server falls
// back to/suggests, exposed standalone for the create form's live chip
// and light toggle default. light is only ever a suggestion; create()
// still requires the operator's explicit flag.
export async function classifyMission(
  goal: string,
): Promise<{ kind: 'coding' | 'general'; light: boolean }> {
  return request<{ kind: 'coding' | 'general'; light: boolean }>('/v1/missions/classify', {
    method: 'POST',
    body: JSON.stringify({ goal }),
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

// sendMissionNote injects operator guidance into a running mission via
// the progress-note pipeline: no state transition, picked up by the
// next worker turn's own packet.
export async function sendMissionNote(id: string, text: string): Promise<void> {
  await request<void>(`/v1/missions/${id}/note`, {
    method: 'POST',
    body: JSON.stringify({ text }),
  })
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

// approveMissionPlan approves a mission parked on plan approval
// (pause_reason: "approval"): the mission moves to phase=generate.
export async function approveMissionPlan(id: string): Promise<void> {
  await request<void>(`/v1/missions/${id}/approve-plan`, { method: 'POST' })
}

// replanMission re-runs the plan phase, folding feedback into the next
// planning prompt: the mission re-parks on plan approval once the new
// plan lands.
export async function replanMission(id: string, feedback?: string): Promise<void> {
  await request<void>(`/v1/missions/${id}/replan`, {
    method: 'POST',
    body: JSON.stringify(feedback ? { feedback } : {}),
  })
}

// rediscoverMission sends a mission parked on plan approval back to
// the discover phase.
export async function rediscoverMission(id: string): Promise<void> {
  await request<void>(`/v1/missions/${id}/rediscover`, { method: 'POST' })
}

// answerMissionQuestion answers a mission parked on ask_user
// (pending_input): mcq/yes_no answers must match the question's own
// options, open accepts any non-empty text, the server validates
// again either way.
export async function answerMissionQuestion(id: string, answer: string): Promise<void> {
  await request<void>(`/v1/missions/${id}/answer`, {
    method: 'POST',
    body: JSON.stringify({ answer }),
  })
}

// pushMission pushes the mission's branch to its worktree's origin
// remote. credentialRef is optional: omitted (undefined) resolves a
// github-connection mission's connector PAT server-side; passing one
// always overrides.
export async function pushMission(
  id: string,
  credentialRef?: string,
): Promise<{ branch: string; remote_host: string }> {
  return request<{ branch: string; remote_host: string }>(`/v1/missions/${id}/push`, {
    method: 'POST',
    body: JSON.stringify(credentialRef ? { credential_ref: credentialRef } : {}),
  })
}

// openMissionPR pushes (idempotent re-push) then opens a pull request
// for a github-connection mission: or returns the existing open PR
// for the same head if GitHub reports one already exists.
export async function openMissionPR(id: string): Promise<{ url: string; number: number }> {
  return request<{ url: string; number: number }>(`/v1/missions/${id}/pr`, { method: 'POST' })
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
// it via a programmatic anchor click: plain hrefs can't carry the
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
    failChat(res.status, message || `request failed (${res.status})`, code)
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

// missionPdfPreviewCap is the same guard, raised for pdf kind: the
// browser's own PDF plugin renders it, not the tab's JS, so a much
// larger file is still safe to hand over as a blob URL.
export const missionPdfPreviewCap = 25_000_000

export class MissionFileTooLargeError extends Error {}

// fetchMissionFileBlob reads a mission file's bytes for in-app
// preview (image/text/markdown/pdf) rather than triggering a save:
// the server forces Content-Type: application/octet-stream on this
// route deliberately (a worker-authored file could be arbitrary
// HTML), so callers must render the bytes themselves, never navigate
// to the URL.
export async function fetchMissionFileBlob(
  id: string,
  path: string,
  cap: number = missionFilePreviewCap,
): Promise<Blob> {
  const res = await fetch(`/v1/missions/${id}/files/${encodeFilePath(path)}`, {
    headers: { Authorization: `Bearer ${getToken()}` },
  })
  if (!res.ok) {
    failChat(res.status, `request failed (${res.status})`)
  }
  const len = res.headers.get('Content-Length')
  if (len && Number(len) > cap) {
    throw new MissionFileTooLargeError('file too large to preview')
  }
  const blob = await res.blob()
  if (blob.size > cap) {
    throw new MissionFileTooLargeError('file too large to preview')
  }
  return blob
}

export async function downloadMissionArchive(id: string): Promise<void> {
  return fetchBlobDownload(`/v1/missions/${id}/archive`, `mission-${id.slice(0, 8)}.zip`)
}

// exportMissionPdf renders a mission's workspace markdown to PDF via
// the pdfgen sidecar. path exports a single file; omitted, it merges
// all workspace markdown into one PDF. cached indicates the content
// hash already matched an existing attachment.
export async function exportMissionPdf(
  id: string,
  path?: string,
): Promise<{ attachment_id: string; cached: boolean }> {
  return request<{ attachment_id: string; cached: boolean }>(`/v1/missions/${id}/export-pdf`, {
    method: 'POST',
    body: JSON.stringify(path ? { path } : {}),
  })
}

// downloadMissionPdfExport fetches the exported PDF attachment and
// saves it under the caller-supplied filename.
export async function downloadMissionPdfExport(attachmentId: string, filename: string): Promise<void> {
  return fetchBlobDownload(`/v1/attachments/${attachmentId}`, filename)
}

// promoteMissionToKB promotes a done mission's markdown artifacts into
// collectionId as kb documents with provenance='mission' (D-081, issue
// #370). Done-only server-side; idempotent (re-promoting replaces
// content rather than duplicating documents).
export async function promoteMissionToKB(
  id: string,
  collectionId: string,
): Promise<{ promoted: number; failed?: string[] }> {
  return request<{ promoted: number; failed?: string[] }>(`/v1/missions/${id}/promote-kb`, {
    method: 'POST',
    body: JSON.stringify({ collection_id: collectionId }),
  })
}

// exportMessagePDF renders one chat message's already-rendered markdown
// into a single-chapter PDF via the pdfgen sidecar. The message content
// travels in the body: it's already in the transcript the client
// holds, no server-side lookup needed.
export async function exportMessagePDF(
  title: string,
  content: string,
): Promise<{ attachment_id: string; cached: boolean }> {
  return request<{ attachment_id: string; cached: boolean }>('/v1/chat/export-pdf', {
    method: 'POST',
    body: JSON.stringify({ title, content }),
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
