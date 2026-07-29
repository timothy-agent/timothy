import { useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { answerPermission, ChatError, chatStream, getTranscript, retryStream } from '../api/client'
import type { ChatEvent } from '../api/types'
import { ActivityPanel } from '../components/Activity'
import { Composer } from '../components/Composer'
import { AssistantMessage, CompactionDivider, InterruptedMessage, UserMessage } from '../components/Message'
import { PermissionModal } from '../components/PermissionModal'
import { Sheet } from '../components/ui/sheet'
import {
  answerPermission as clearPermission,
  applyEvent,
  emptyAssistant,
  type AssistantState,
} from '../lib/chat'
import { useSessions } from '../lib/sessions'
import { fromTranscript, type ChatItem } from '../lib/transcript'
import type { ChatIntent } from './Home'

const agentKey = 'timothy.agent'
const routeKey = 'timothy.route'

export function Chat({
  onNeedToken,
  basePath = '/chat',
  lockedSkillHint,
  emptyHeading = 'What can I help with?',
  emptySubtext = 'Ask anything. The agent picker chooses who serves you.',
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
  // Locked pages fix their own agent and never touch the shared
  // localStorage preference — a research page reading (or clobbering)
  // whatever agent general chat last used would be a leak either
  // direction. Empty string = the server-side default agent.
  const [agent, setAgent] = useState(
    () => (lockedSkillHint ? 'researcher' : (localStorage.getItem(agentKey) ?? '')),
  )
  // Same locked-page carve-out as agent: a dedicated page fixes its
  // own route (or leaves it Auto) and never touches the shared
  // preference. Empty string = Auto (the agent's own route, or the
  // server default).
  const [route, setRoute] = useState(
    () => (lockedSkillHint ? '' : (localStorage.getItem(routeKey) ?? '')),
  )
  // A skill picked from the home screen: pinned, not text the user
  // can accidentally mangle. Rides every turn until removed. Locked
  // pages skip this state entirely and always send lockedSkillHint.
  const [skillHint, setSkillHint] = useState<string | undefined>(lockedSkillHint)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [streaming, setStreaming] = useState(false)
  // Id of the assistant item whose Activity panel is open, if any. The
  // panel content is derived from `items` below rather than captured
  // once, so it keeps updating live while that turn is still streaming.
  const [activityId, setActivityId] = useState<string | null>(null)
  const sessionRef = useRef<string | undefined>(routeSession)
  // Session ids this page itself adopted mid-stream (via navigate):
  // the resume effect must not clobber the live stream with a replay.
  const adoptedRef = useRef<string | null>(null)
  const intentConsumedRef = useRef(false)
  const bottomRef = useRef<HTMLDivElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  // Whether the view is pinned to the bottom. Scrolling up releases
  // the pin so a streaming answer stops yanking the view back down;
  // scrolling back near the bottom re-engages it.
  const pinnedRef = useRef(true)
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
        if (session.agent) setAgent(session.agent)
        setRoute(session.last_route ?? '')
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
    if (pinnedRef.current) bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [items])

  const trackPin = () => {
    const el = listRef.current
    if (!el) return
    pinnedRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80
  }

  const pickAgent = (a: string) => {
    setAgent(a)
    localStorage.setItem(agentKey, a)
  }

  const pickRoute = (r: string) => {
    setRoute(r)
    localStorage.setItem(routeKey, r)
  }

  const updateLast = (fn: (m: AssistantState) => AssistantState) =>
    setItems((prev) => {
      const next = [...prev]
      const last = next[next.length - 1]
      if (last?.role === 'assistant')
        next[next.length - 1] = { ...fn(last), id: last.id, role: 'assistant' }
      return next
    })

  const sendMessage = async (
    text: string,
    agentName: string,
    hint = skillHint,
    routeName = route,
  ) => {
    const message = text.trim()
    if (!message || streaming) return
    setDraft('')
    setStreaming(true)
    pinnedRef.current = true // sending always re-follows the answer
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
        {
          session_id: sessionRef.current,
          message,
          agent: agentName,
          route: routeName || undefined,
          skill_hint: hint,
        },
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

  const send = () => void sendMessage(draft, agent)

  // retryLast re-runs the last (failed) turn: the session already
  // carries the dangling user message server-side (chat.Service.Retry),
  // so this never sends a new user message like sendMessage does. Two
  // shapes reach here: an in-session failure, where the trailing item
  // is still the assistant one from the failed attempt and gets reset
  // in place; and a reload after a failure, where the transcript ends
  // at the dangling user message (no assistant item survived), so one
  // is appended for the stream to render into.
  const retryLast = async () => {
    const sessionId = sessionRef.current
    if (!sessionId || streaming) return
    setStreaming(true)
    pinnedRef.current = true
    setItems((prev) => {
      const next = [...prev]
      const last = next[next.length - 1]
      if (last?.role === 'assistant') next[next.length - 1] = { ...emptyAssistant(), id: last.id, role: 'assistant' }
      else next.push({ id: crypto.randomUUID(), role: 'assistant', ...emptyAssistant() })
      return next
    })

    const controller = new AbortController()
    abortRef.current = controller
    try {
      await retryStream(
        sessionId,
        (ev: ChatEvent) => updateLast((m) => applyEvent(m, ev)),
        { signal: controller.signal },
      )
      refresh()
    } catch (err) {
      if (controller.signal.aborted) return
      if (err instanceof ChatError) {
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

  // Consume a home-screen intent exactly once: `send` fires the
  // message immediately, `draft` prefills the composer, `skillHint`
  // pins the chip. sendMessage takes hint explicitly here rather than
  // relying on the skillHint state landing before the call — setState
  // doesn't take effect until the next render.
  useEffect(() => {
    if (lockedSkillHint || intentConsumedRef.current) return
    const intent = location.state as ChatIntent | null
    if (!intent || (!intent.send && !intent.draft && !intent.agent && !intent.route && !intent.skillHint))
      return
    intentConsumedRef.current = true
    window.history.replaceState(null, '') // don't refire on back/refresh
    const who = intent.agent ?? agent
    const whichRoute = intent.route ?? route
    if (intent.agent) pickAgent(intent.agent)
    if (intent.route) pickRoute(intent.route)
    if (intent.skillHint) setSkillHint(intent.skillHint)
    if (intent.send) void sendMessage(intent.send, who, intent.skillHint, whichRoute)
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
    answerPermission(id, decision).catch((err: unknown) => {
      // A replayed ask whose turn died (brain restart) answers 404
      // "unknown or already-answered" — the prompt is already gone
      // locally (above), so just tell the user why nothing happened.
      // Any other error is the same 10-minute-timeout-resolves-as-deny
      // case as before: nothing useful to render.
      if (err instanceof ChatError && err.status === 404) {
        toast.error('This request is no longer waiting for a response')
      }
    })
  }

  // Re-derived from `items` every render (not captured on open) so the
  // panel keeps showing live tool/reasoning state while its turn streams.
  const activityItem = items.find((i) => i.id === activityId)
  const activityMsg = activityItem?.role === 'assistant' ? activityItem : undefined

  return (
    <div className="flex h-full flex-col">
      {pendingPermission && <PermissionModal request={pendingPermission} onDecision={decide} />}
      <Sheet open={activityMsg !== undefined} onOpenChange={(open) => !open && setActivityId(null)}>
        {activityMsg && <ActivityPanel msg={activityMsg} />}
      </Sheet>
      <div ref={listRef} onScroll={trackPin} className="flex-1 space-y-6 overflow-y-auto py-6">
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
        {items.map((item, i) => {
          switch (item.role) {
            case 'user':
              return (
                <UserMessage
                  key={item.id}
                  text={item.text}
                  // A trailing user message means the turn died before any
                  // assistant event reached the transcript — retry re-runs
                  // it server-side, same trailing-only condition as above.
                  onRetry={i === items.length - 1 && !streaming ? retryLast : undefined}
                />
              )
            case 'compaction':
              return <CompactionDivider key={item.id} text={item.text} />
            case 'interrupted':
              return <InterruptedMessage key={item.id} text={item.text} />
            default:
              return (
                <AssistantMessage
                  key={item.id}
                  msg={item}
                  // Retry only ever targets the trailing dangling turn
                  // (the session's last event server-side) — never a
                  // mid-transcript message.
                  onRetry={i === items.length - 1 && !streaming ? retryLast : undefined}
                  onShowActivity={() => setActivityId(item.id)}
                />
              )
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
          agent={agent}
          onAgent={pickAgent}
          route={route}
          onRoute={pickRoute}
          hidePicker={Boolean(lockedSkillHint)}
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
