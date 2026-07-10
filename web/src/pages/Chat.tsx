import { ArrowUpIcon } from '@heroicons/react/20/solid'
import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router'
import { ChatError, chatStream, getTranscript } from '../api/client'
import type { ChatEvent } from '../api/types'
import { CategoryPicker } from '../components/CategoryPicker'
import {
  AssistantMessage,
  CompactionDivider,
  InterruptedMessage,
  UserMessage,
} from '../components/Message'
import { applyEvent, categories, type AssistantState } from '../lib/chat'
import { useSessions } from '../lib/sessions'
import { fromTranscript, type ChatItem } from '../lib/transcript'

const categoryKey = 'timothy.category'

export function Chat({ onNeedToken }: { onNeedToken: () => void }) {
  const { id: routeSession } = useParams()
  const navigate = useNavigate()
  const { refresh } = useSessions()
  const [items, setItems] = useState<ChatItem[]>([])
  const [draft, setDraft] = useState('')
  const [category, setCategory] = useState(
    () => localStorage.getItem(categoryKey) ?? categories[0],
  )
  const [loadError, setLoadError] = useState<string | null>(null)
  const [streaming, setStreaming] = useState(false)
  const sessionRef = useRef<string | undefined>(routeSession)
  // Session ids this page itself adopted mid-stream (via navigate):
  // the resume effect must not clobber the live stream with a replay.
  const adoptedRef = useRef<string | null>(null)
  const bottomRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const abortRef = useRef<AbortController | null>(null)

  // Cancel any in-flight stream when the page unmounts (route change).
  useEffect(() => () => abortRef.current?.abort(), [])

  // Resume: replay the session's transcript projection, exactly as the
  // server recorded it, and restore its last task category.
  useEffect(() => {
    if (adoptedRef.current !== null && adoptedRef.current === routeSession) {
      sessionRef.current = routeSession
      return // our own mid-stream URL adoption, not a navigation
    }
    // Real navigation: a stream still running belongs to the previous
    // session — kill it before it writes into this one's transcript.
    if (abortRef.current) {
      abortRef.current.abort()
      abortRef.current = null
      setStreaming(false)
    }
    sessionRef.current = routeSession
    setLoadError(null)
    if (!routeSession) {
      setItems([])
      adoptedRef.current = null
      return
    }
    adoptedRef.current = null
    let stale = false
    getTranscript(routeSession)
      .then(({ session, items }) => {
        if (stale) return
        setItems(fromTranscript(items))
        if (session.last_category) setCategory(session.last_category)
      })
      .catch((err: unknown) => {
        if (stale) return
        if (err instanceof ChatError && (err.status === 401 || err.status === 503)) onNeedToken()
        setLoadError(err instanceof Error ? err.message : String(err))
      })
    return () => {
      stale = true
    }
  }, [routeSession, onNeedToken])

  // Auto-grow the composer up to a cap, then scroll inside it.
  const autogrow = () => {
    const el = inputRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 200)}px`
  }

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [items])

  const pickCategory = (c: string) => {
    setCategory(c)
    localStorage.setItem(categoryKey, c)
  }

  const updateLast = (fn: (m: AssistantState) => AssistantState) =>
    setItems((prev) => {
      const next = [...prev]
      const last = next[next.length - 1]
      if (last?.role === 'assistant')
        next[next.length - 1] = { ...fn(last), id: last.id, role: 'assistant' }
      return next
    })

  const send = async () => {
    const message = draft.trim()
    if (!message || streaming) return
    setDraft('')
    requestAnimationFrame(autogrow)
    setStreaming(true)
    setItems((prev) => [
      ...prev,
      { id: crypto.randomUUID(), role: 'user', text: message },
      {
        id: crypto.randomUUID(),
        role: 'assistant',
        text: '',
        reasoning: '',
        notices: [],
        streaming: true,
      },
    ])

    const controller = new AbortController()
    abortRef.current = controller
    const adoptSession = (id: string) => {
      if (sessionRef.current === id) return
      sessionRef.current = id
      adoptedRef.current = id
      // Same route pattern serves / and /sessions/:id, so this only
      // re-renders — the live stream keeps its component state.
      navigate(`/sessions/${id}`, { replace: true })
    }
    try {
      await chatStream(
        { session_id: sessionRef.current, message, task_category: category },
        (ev: ChatEvent) => {
          if (ev.type === 'meta') adoptSession(ev.session_id)
          updateLast((m) => applyEvent(m, ev))
        },
        {
          signal: controller.signal,
          onSession: adoptSession,
        },
      )
      refresh() // updated_at moved; the first exchange also titles it
    } catch (err) {
      if (controller.signal.aborted) return // unmounted; nothing to render
      if (err instanceof ChatError) {
        // Brain may have created the session before failing: keep it.
        if (err.sessionId) adoptSession(err.sessionId)
        if (err.status === 401 || err.status === 503) onNeedToken()
        updateLast((m) => ({ ...m, streaming: false, error: err.message }))
      } else {
        updateLast((m) => ({ ...m, streaming: false, error: String(err) }))
      }
    } finally {
      abortRef.current = null
      if (!controller.signal.aborted) {
        setStreaming(false)
        updateLast((m) => ({ ...m, streaming: false }))
      }
    }
  }

  return (
    <div className="flex h-full flex-col">
      <div className="flex-1 space-y-6 overflow-y-auto py-6">
        {items.length === 0 && !loadError && (
          <div className="mt-24 text-center">
            <h2 className="text-xl font-semibold text-zinc-700 dark:text-zinc-200">
              What can I help with?
            </h2>
            <p className="mt-2 text-sm text-zinc-400">
              Ask anything. The category picker steers which model serves you.
            </p>
          </div>
        )}
        {loadError && (
          <div className="mt-24 text-center text-sm text-red-500">
            Could not load this session: {loadError}
          </div>
        )}
        {items.map((item) => {
          switch (item.role) {
            case 'user':
              return <UserMessage key={item.id} text={item.text} />
            case 'compaction':
              return <CompactionDivider key={item.id} text={item.text} />
            case 'interrupted':
              return <InterruptedMessage key={item.id} text={item.text} />
            default:
              return <AssistantMessage key={item.id} msg={item} />
          }
        })}
        <div ref={bottomRef} />
      </div>

      <form
        className="pb-3"
        onSubmit={(e) => {
          e.preventDefault()
          void send()
        }}
      >
        <div className="rounded-2xl border border-zinc-950/10 bg-white shadow-sm transition focus-within:border-blue-500/50 focus-within:ring-4 focus-within:ring-blue-500/10 dark:border-white/10 dark:bg-zinc-800/60 dark:focus-within:border-blue-400/40">
          <textarea
            ref={inputRef}
            aria-label="Message"
            rows={1}
            value={draft}
            placeholder="Message Timothy…"
            className="max-h-50 w-full resize-none bg-transparent px-4 pt-3.5 pb-1.5 text-base/6 text-zinc-900 outline-none placeholder:text-zinc-400 sm:text-sm/6 dark:text-white dark:placeholder:text-zinc-500"
            onChange={(e) => {
              setDraft(e.target.value)
              autogrow()
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault()
                void send()
              }
            }}
          />
          <div className="flex items-center justify-between gap-2 px-2.5 pb-2.5">
            <CategoryPicker value={category} onChange={pickCategory} />
            <button
              type="submit"
              aria-label="Send"
              disabled={streaming || draft.trim() === ''}
              className="flex size-9 shrink-0 items-center justify-center rounded-full bg-blue-600 text-white transition hover:bg-blue-500 disabled:bg-zinc-200 disabled:text-zinc-400 dark:disabled:bg-zinc-700 dark:disabled:text-zinc-500"
            >
              <ArrowUpIcon className="size-4" />
            </button>
          </div>
        </div>
        <p className="mt-2 text-center text-xs text-zinc-400 dark:text-zinc-500">
          Enter to send · Shift+Enter for a new line
        </p>
      </form>
    </div>
  )
}
