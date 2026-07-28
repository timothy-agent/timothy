import { createSSEParser, getToken } from '../api/client'

export interface Signal {
  kind: string
  id: string
}

// EventFrame is the raw JSON payload of one /v1/events "data:" line —
// createSSEParser only sees "data:" lines (it ignores the SSE
// "event:" line), so "type" rides inside the JSON itself, same
// convention as chat's terminal meta event.
interface EventFrame {
  type: 'ready' | 'signal'
  kind?: string
  id?: string
}

const initialBackoffMs = 1000
const maxBackoffMs = 15000

// subscribeEvents replaces the old 5s poll: it opens GET /v1/events
// (fetch, not EventSource — EventSource can't send a Bearer header)
// and reads it the same way chatStream/postSSE already does, via
// createSSEParser over the response body's reader. onSignal fires for
// every mission/notification change hint; onReady fires once per
// (re)connect (initial connect and every reconnect after a drop), so
// callers refetch anything they might have missed while disconnected.
// Returns an unsubscribe function: aborts the in-flight request and
// stops any pending reconnect.
export function subscribeEvents(onSignal: (s: Signal) => void, onReady?: () => void): () => void {
  const controller = new AbortController()
  let stopped = false
  let backoffMs = initialBackoffMs
  let retryTimer: ReturnType<typeof setTimeout> | undefined

  async function connect() {
    if (stopped) return
    try {
      const res = await fetch('/v1/events', {
        headers: { Authorization: `Bearer ${getToken()}` },
        signal: controller.signal,
      })
      if (!res.ok || !res.body) throw new Error(`events stream failed (${res.status})`)

      backoffMs = initialBackoffMs
      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      const parser = createSSEParser<EventFrame>((e) => {
        if (e.type === 'ready') {
          onReady?.()
        } else if (e.type === 'signal' && e.kind && e.id) {
          onSignal({ kind: e.kind, id: e.id })
        }
      })
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        parser.feed(decoder.decode(value, { stream: true }))
      }
      parser.end()
    } catch {
      // Aborted (unsubscribe called) or a transport error — either
      // way, fall through to reconnect-with-backoff unless stopped.
    }
    if (stopped) return
    retryTimer = setTimeout(() => {
      void connect()
    }, backoffMs)
    backoffMs = Math.min(backoffMs * 2, maxBackoffMs)
  }

  void connect()

  return () => {
    stopped = true
    controller.abort()
    if (retryTimer) clearTimeout(retryTimer)
  }
}
