import type { ChatEvent, ChatRequest, SessionMeta, Transcript } from './types'

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
