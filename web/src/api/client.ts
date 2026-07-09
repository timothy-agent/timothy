import type { ChatEvent, ChatRequest } from './types'

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

// chatStream posts one turn and delivers every SSE event, ending with
// the terminal meta event. Throws on non-200 responses (auth, config).
export async function chatStream(
  req: ChatRequest,
  onEvent: (ev: ChatEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch('/v1/chat', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${getToken()}`,
    },
    body: JSON.stringify(req),
    signal,
  })
  if (!res.ok || !res.body) {
    const body = await res.text().catch(() => '')
    throw new Error(`chat failed (${res.status}): ${body}`)
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
