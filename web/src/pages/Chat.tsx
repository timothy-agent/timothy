import { useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router'
import { answerPermission, ChatError, chatStream, getTranscript } from '../api/client'
import type { ChatEvent } from '../api/types'
import { Composer } from '../components/Composer'
import {
  AssistantMessage,
  CompactionDivider,
  InterruptedMessage,
  ToolBlock,
  UserMessage,
} from '../components/Message'
import { PermissionModal } from '../components/PermissionModal'
import {
  answerPermission as clearPermission,
  applyEvent,
  categories,
  emptyAssistant,
  type AssistantState,
} from '../lib/chat'
import { useSessions } from '../lib/sessions'
import { fromTranscript, type ChatItem } from '../lib/transcript'
import type { ChatIntent } from './Home'

const categoryKey = 'timothy.category'

export function Chat({
  onNeedToken,
  basePath = '/chat',
  lockedSkillHint,
  emptyHeading = 'What can I help with?',
  emptySubtext = 'Ask anything. The category picker steers which model serves you.',
  placeholder,
}: {
  onNeedToken: () => void
  // Route prefix for session-id adoption (e.g. "/research" for the
  // locked research page) — kept in sync with the <Route> that mounts
  // this component so mid-stream URL adoption lands on the right page.
  basePath?: string
  // When set, the skill is pinned for every turn and cannot be
  // removed or overridden by a home-screen intent — a dedicated,
  // single-purpose page rather than general chat with a chip.
  lockedSkillHint?: string
  emptyHeading?: string
  emptySubtext?: string
  placeholder?: string
}) {
  const { id: routeSession } = useParams()
  const navigate = useNavigate()
  const location = useLocation()
  const { refresh } = useSessions()
  const [items, setItems] = useState<ChatItem[]>([])
  const [draft, setDraft] = useState('')
  // Locked pages fix their own category and never touch the shared
  // localStorage preference — a research page reading (or clobbering)
  // whatever category general chat last used would be a leak either
  // direction.
  const [category, setCategory] = useState(
    () => (lockedSkillHint ? 'research' : (localStorage.getItem(categoryKey) ?? categories[0])),
  )
  // A skill picked from the home screen: pinned, not text the user
  // can accidentally mangle. Rides every turn until removed. Locked
  // pages skip this state entirely and always send lockedSkillHint.
  const [skillHint, setSkillHint] = useState<string | undefined>(lockedSkillHint)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [streaming, setStreaming] = useState(false)
  const sessionRef = useRef<string | undefined>(routeSession)
  // Session ids this page itself adopted mid-stream (via navigate):
  // the resume effect must not clobber the live stream with a replay.
  const adoptedRef = useRef<string | null>(null)
  const intentConsumedRef = useRef(false)
  const bottomRef = useRef<HTMLDivElement>(null)
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

  const sendMessage = async (text: string, cat: string, hint = skillHint) => {
    const message = text.trim()
    if (!message || streaming) return
    setDraft('')
    setStreaming(true)
    setItems((prev) => [
      ...prev,
      { id: crypto.randomUUID(), role: 'user', text: message },
      { id: crypto.randomUUID(), role: 'assistant', ...emptyAssistant() },
    ])

    const controller = new AbortController()
    abortRef.current = controller
    const adoptSession = (id: string) => {
      if (sessionRef.current === id) return
      sessionRef.current = id
      adoptedRef.current = id
      // Same route pattern serves basePath and basePath/:id, so this
      // only re-renders — the live stream keeps its component state.
      navigate(`${basePath}/${id}`, { replace: true })
    }
    try {
      await chatStream(
        { session_id: sessionRef.current, message, task_category: cat, skill_hint: hint },
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

  const send = () => void sendMessage(draft, category)

  // Consume a home-screen intent exactly once: `send` fires the
  // message immediately, `draft` prefills the composer, `skillHint`
  // pins the chip. sendMessage takes hint explicitly here rather than
  // relying on the skillHint state landing before the call — setState
  // doesn't take effect until the next render.
  useEffect(() => {
    if (lockedSkillHint || intentConsumedRef.current) return
    const intent = location.state as ChatIntent | null
    if (!intent || (!intent.send && !intent.draft && !intent.category && !intent.skillHint)) return
    intentConsumedRef.current = true
    window.history.replaceState(null, '') // don't refire on back/refresh
    const cat = intent.category ?? category
    if (intent.category) pickCategory(intent.category)
    if (intent.skillHint) setSkillHint(intent.skillHint)
    if (intent.send) void sendMessage(intent.send, cat, intent.skillHint)
    else if (intent.draft) setDraft(intent.draft)
    // Mount-time only by design: the intent rides the navigation that
    // created this page instance; the ref guards strict-mode replays.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // The permission modal shows the oldest unanswered prompt of the
  // live turn; answering posts the decision and drops it locally.
  const last = items[items.length - 1]
  const pendingPermission = last?.role === 'assistant' ? last.permissions[0] : undefined
  const decide = (id: string, decision: 'once' | 'session' | 'deny') => {
    updateLast((m) => clearPermission(m, id))
    answerPermission(id, decision).catch(() => {
      // The 10-minute server timeout resolves an undeliverable
      // decision as deny; nothing useful to render here.
    })
  }

  return (
    <div className="flex h-full flex-col">
      {pendingPermission && <PermissionModal request={pendingPermission} onDecision={decide} />}
      <div className="flex-1 space-y-6 overflow-y-auto py-6">
        {items.length === 0 && !loadError && (
          <div className="mt-24 text-center">
            <h2 className="text-xl font-semibold text-zinc-700 dark:text-zinc-200">
              {emptyHeading}
            </h2>
            <p className="mt-2 text-sm text-zinc-400">{emptySubtext}</p>
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
            case 'tool':
              return <ToolBlock key={item.id} tool={item.tool} />
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
          send()
        }}
      >
        <Composer
          draft={draft}
          onDraft={setDraft}
          onSend={send}
          category={category}
          onCategory={pickCategory}
          hideCategoryPicker={Boolean(lockedSkillHint)}
          skillHint={skillHint}
          onRemoveSkillHint={lockedSkillHint ? undefined : () => setSkillHint(undefined)}
          disabled={streaming}
          placeholder={placeholder}
        />
        <p className="mt-2 text-center text-xs text-zinc-400 dark:text-zinc-500">
          Enter to send · Shift+Enter for a new line
        </p>
      </form>
    </div>
  )
}
