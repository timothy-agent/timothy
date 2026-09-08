import { useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import { ArrowDown } from 'lucide-react'
import {
  answerPermission,
  ChatError,
  chatStream,
  errorText,
  getTranscript,
  retryStream,
  setSessionKnowledge,
  stopTurn,
  streamLive,
} from '../api/client'
import type { ChatEvent, Reference } from '../api/types'
import { ActivityPanel } from '../components/Activity'
import { useAgents } from '../components/AgentPicker'
import { AgentStatusLine } from '../components/chat/AgentStatusLine'
import { ApprovalDialog } from '../components/chat/ApprovalCard'
import { Composer, isDocumentAttachment, type PendingAttachment } from '../components/Composer'
import { AssistantMessage, CompactionDivider, ErrorMessage, InterruptedMessage, UserMessage } from '../components/Message'
import { Alert, AlertTitle } from '../components/ui/alert'
import { Button } from '../components/ui/button'
import { Kbd } from '../components/timothy/kbd'
import { Sheet } from '../components/ui/sheet'
import { TooltipProvider } from '../components/ui/tooltip'
import {
  answerPermission as clearPermission,
  applyEvent,
  emptyAssistant,
  type AssistantState,
} from '../lib/chat'
import { agentPhaseFromState } from '../lib/chatUi'
import { subscribeEvents } from '../lib/events'
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
  // locked research page): kept in sync with the <Route> that mounts
  // this component so mid-stream URL adoption lands on the right page.
  basePath?: string
  // When set, the skill is pinned for every turn and cannot be
  // removed or overridden by a home-screen intent: a dedicated,
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
  const agents = useAgents()
  const [items, setItems] = useState<ChatItem[]>([])
  const [draft, setDraft] = useState('')
  const [attachments, setAttachments] = useState<PendingAttachment[]>([])
  // Optimistic sends' local object URLs, keyed by chat item id, so a
  // just-sent message's thumbnails render instantly (UserMessage's
  // localUrls prop) without round-tripping through AuthedImage's
  // authed fetch: cleared per item only on unmount of the page, since
  // a resumed/replayed session recreates items server-side anyway.
  const localUrlsRef = useRef(new Map<string, Map<string, string>>())
  // Locked pages fix their own agent and never touch the shared
  // localStorage preference: a research page reading (or clobbering)
  // whatever agent general chat last used would be a leak either
  // direction. Empty string = the server-side default agent, the only
  // seeded one; its skills allowlist covers every shipped pack, so a
  // locked page's skill hint always passes the allowlist gate.
  const [agent, setAgent] = useState(
    () => (lockedSkillHint ? '' : (localStorage.getItem(agentKey) ?? '')),
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
  // Collection names pinned via #mention chips. Seeded from the
  // session's `knowledge` on load; local-only until the first send on
  // a brand-new chat (no session id yet).
  const [knowledge, setKnowledge] = useState<string[]>([])
  // Mission/chat/document references picked via #mention chips:
  // component state only, never persisted (unlike knowledge): cleared
  // on send, same as attachments.
  const [references, setReferences] = useState<Reference[]>([])
  // The serving agent's own bound collections: always searched, never
  // pinned by the user. Re-derived from the live agent selection (not
  // snapshotted) so switching agents mid-session updates the chips.
  // Same fallback as AgentRoutePicker: an empty/unmatched agent id
  // resolves to the default agent, the one that actually serves it.
  const servingAgent = agents.find((a) => a.id === agent) ?? agents.find((a) => a.is_default)
  const agentKnowledge = servingAgent?.knowledge ?? []
  const [loadError, setLoadError] = useState<string | null>(null)
  const [streaming, setStreaming] = useState(false)
  // Id of the assistant item whose Activity panel is open, if any. The
  // panel content is derived from `items` below rather than captured
  // once, so it keeps updating live while that turn is still streaming.
  const [activityId, setActivityId] = useState<string | null>(null)
  // Mirrors pendingPermission arriving while the transcript is
  // scrolled away from it (pinnedRef false): shows the modal
  // ApprovalDialog on top of the inline card. Closing it (without
  // deciding) drops back to just the inline card + status line.
  const [showApprovalDialog, setShowApprovalDialog] = useState(false)
  // The status line's "done" summary lingers 5s after a turn finishes
  // (contract 14.3), then the line reverts to idle.
  const [justDone, setJustDone] = useState<{ durationMs?: number; toolCount: number } | null>(null)
  const doneTimerRef = useRef<number | undefined>(undefined)
  const sessionRef = useRef<string | undefined>(routeSession)
  // Session ids this page itself adopted mid-stream (via navigate):
  // the resume effect must not clobber the live stream with a replay.
  const adoptedRef = useRef<string | null>(null)
  const intentConsumedRef = useRef(false)
  const bottomRef = useRef<HTMLDivElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  // Whether the view is pinned to the bottom. Scrolling up releases
  // the pin so a streaming answer stops yanking the view back down;
  // scrolling back near the bottom re-engages it. Mirrored into state
  // so the Jump to latest button and the approval dialog gate can
  // react to it.
  const pinnedRef = useRef(true)
  const [pinned, setPinned] = useState(true)
  const abortRef = useRef<AbortController | null>(null)

  // Cancel any in-flight stream when the page unmounts (route change).
  useEffect(() => () => abortRef.current?.abort(), [])

  // Revoke every optimistic-send object URL when the page unmounts:
  // they only exist to make a just-sent thumbnail instant, and a
  // route change means AuthedImage's authed fetch takes over on
  // remount (resume replays the transcript from scratch either way).
  useEffect(
    () => () => {
      for (const urls of localUrlsRef.current.values()) {
        for (const url of urls.values()) URL.revokeObjectURL(url)
      }
    },
    [],
  )

  // Resume: replay the session's transcript projection, exactly as the
  // server recorded it, and restore its last task category.
  useEffect(() => {
    if (adoptedRef.current !== null && adoptedRef.current === routeSession) {
      sessionRef.current = routeSession
      return // our own mid-stream URL adoption, not a navigation
    }
    // Real navigation: a stream still running belongs to the previous
    // session: kill it before it writes into this one's transcript.
    if (abortRef.current) {
      abortRef.current.abort()
      abortRef.current = null
      setStreaming(false)
    }
    sessionRef.current = routeSession
    setLoadError(null)
    if (!routeSession) {
      setItems([])
      setKnowledge([])
      adoptedRef.current = null
      return
    }
    adoptedRef.current = null
    let stale = false
    getTranscript(routeSession)
      .then(({ session, items, turn_active }) => {
        if (stale) return
        setItems(fromTranscript(items))
        if (session.agent) setAgent(session.agent)
        setRoute(session.last_route ?? '')
        setKnowledge(session.knowledge ?? [])
        // A turn was already streaming when this tab opened the session
        // (opened mid-turn, or a reload during one): attach to it live
        // instead of leaving the transcript's replay looking stale.
        if (turn_active) attachLive(routeSession)
      })
      .catch((err: unknown) => {
        if (stale) return
        if (err instanceof ChatError && (err.status === 401 || err.status === 503)) onNeedToken()
        setLoadError(errorText(err))
      })
    return () => {
      stale = true
    }
    // attachLive is stable across renders in effect (closes over setters
    // only) but isn't itself memoized: omitted deliberately, same
    // pattern as the intent-consuming effect below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [routeSession, onNeedToken])

  // Tier 1 fallback: while NOT attached to a live stream for the open
  // session, a "session" signal means some turn elsewhere finished (or
  // this tab missed the transition): refetch the transcript so a tab
  // that never got to attach live still catches up promptly instead of
  // waiting on the next navigation. Attached tabs skip this: they're
  // already getting every event live, and a mid-stream refetch would
  // race the reducer's own state.
  useEffect(() => {
    return subscribeEvents((sig) => {
      if (sig.kind !== 'session' || sig.id !== sessionRef.current) return
      if (streaming) return
      getTranscript(sig.id)
        .then(({ items }) => setItems(fromTranscript(items)))
        .catch(() => undefined)
    })
  }, [streaming])

  useEffect(() => {
    if (pinnedRef.current) bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [items])

  // setPin updates the ref (read synchronously by effects/callbacks
  // elsewhere) and the mirrored state (read by render) together.
  const setPin = (v: boolean) => {
    pinnedRef.current = v
    setPinned(v)
  }

  const trackPin = () => {
    const el = listRef.current
    if (!el) return
    setPin(el.scrollHeight - el.scrollTop - el.clientHeight < 80)
  }

  // updateKnowledge handles both the mention popup's add (local only,
  // rides the next send) and a chip's remove button. A removal on an
  // existing session also persists immediately via PUT: the chip
  // otherwise reappears next time the session is opened.
  const updateKnowledge = (next: string[]) => {
    const removed = next.length < knowledge.length
    setKnowledge(next)
    const sessionId = sessionRef.current
    if (removed && sessionId) {
      setSessionKnowledge(sessionId, next).catch(() => {
        toast.error('Could not update knowledge')
        setKnowledge(knowledge) // roll back the optimistic removal
      })
    }
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

  // attachLive reattaches to a turn that was already streaming when
  // this tab opened the session (turn_active from getTranscript): a
  // trailing 'interrupted' item is the server's replay of a mid-turn
  // pending_state checkpoint: without this it would sit there forever
  // looking dead. Replacing it with a live streaming assistant item and
  // feeding streamLive's events through the SAME applyEvent reducer
  // sendMessage uses makes a reattached turn indistinguishable from one
  // this tab started itself: streaming indicator, activity line,
  // permission modal all just work. The replay buffer already contains
  // every chunk that built up the interrupted partial, so the new item
  // starts empty rather than double-seeding with the persisted text.
  // maxLiveReconnects caps how many times a single dropped connection
  // retries attachLive before giving up: a turn genuinely gone (not
  // just this subscriber's connection) would otherwise loop forever.
  const maxLiveReconnects = 5

  const attachLive = (sessionId: string, reconnectAttempt = 0) => {
    setStreaming(true)
    setPin(true)
    if (reconnectAttempt === 0) {
      setItems((prev) => {
        const next = [...prev]
        const last = next[next.length - 1]
        if (last && (last.role === 'interrupted' || last.role === 'assistant'))
          next[next.length - 1] = { id: last.id, role: 'assistant', ...emptyAssistant() }
        else next.push({ id: crypto.randomUUID(), role: 'assistant', ...emptyAssistant() })
        return next
      })
    }

    const controller = new AbortController()
    abortRef.current = controller
    let sawTerminal = false
    streamLive(
      sessionId,
      (ev: ChatEvent) => {
        if (ev.type === 'meta') sawTerminal = true
        updateLast((m) => applyEvent(m, ev))
      },
      controller.signal,
    )
      .then(() => {
        if (controller.signal.aborted) return
        // The live stream can end without a terminal meta event if this
        // subscriber got dropped for lagging (turnBroadcaster's
        // drop-on-full) rather than the turn actually finishing: a
        // one-shot refetch recovers either way: either it shows the
        // now-completed turn, or it shows the turn still running and
        // the next session signal (Tier 1) prompts another refetch.
        if (!sawTerminal) {
          getTranscript(sessionId)
            .then(({ items }) => setItems(fromTranscript(items)))
            .catch(() => undefined)
        }
        refresh()
      })
      .catch((err: unknown) => {
        if (controller.signal.aborted) return
        if (err instanceof ChatError) {
          // A 404 (no_active_turn) means the turn finished in the gap
          // between getTranscript and this attach: refetch once to pick
          // up the now-completed transcript rather than leaving the
          // freshly-blanked item stuck empty.
          getTranscript(sessionId)
            .then(({ items }) => setItems(fromTranscript(items)))
            .catch(() => undefined)
          if (err.status === 401 || err.status === 503) onNeedToken()
          return
        }
        // A transport-level failure (dropped connection, proxy timeout)
        // rather than a definite server response: the turn is very
        // likely still running server-side (D-042), so reattach instead
        // of treating this as final. abortRef must point at the retry's
        // own controller, not get cleared by this attempt's finally.
        if (reconnectAttempt < maxLiveReconnects) {
          attachLive(sessionId, reconnectAttempt + 1)
          return
        }
        getTranscript(sessionId)
          .then(({ items }) => setItems(fromTranscript(items)))
          .catch(() => undefined)
      })
      .finally(() => {
        if (abortRef.current !== controller) return
        abortRef.current = null
        if (!controller.signal.aborted) {
          setStreaming(false)
          updateLast((m) => ({ ...m, streaming: false }))
        }
      })
  }

  const sendMessage = async (
    text: string,
    agentName: string,
    hint = skillHint,
    routeName = route,
    sentAttachments: PendingAttachment[] = [],
    sentKnowledge = knowledge,
    sentReferences = references,
  ) => {
    const message = text.trim()
    // Uploads still in flight aren't sendable ids yet: only completed
    // ones ride the request; the composer's cap+toast already stops a
    // user from expecting an in-flight one to count.
    const ready = sentAttachments.filter((a) => !a.uploading)
    if ((!message && ready.length === 0) || streaming) return
    setDraft('')
    setAttachments([])
    setReferences([])
    setStreaming(true)
    setPin(true) // sending always re-follows the answer
    const userItemId = crypto.randomUUID()
    if (ready.length > 0) {
      localUrlsRef.current.set(userItemId, new Map(ready.map((a) => [a.id, a.previewUrl])))
    }
    const readyImages = ready.filter((a) => a.mime.startsWith('image/'))
    const readyDocuments = ready.filter((a) => isDocumentAttachment(a.mime))
    setItems((prev) => [
      ...prev,
      {
        id: userItemId,
        role: 'user',
        text: message,
        images:
          readyImages.length > 0
            ? readyImages.map((a) => ({ id: a.id, mime: a.mime, name: a.name }))
            : undefined,
        documents:
          readyDocuments.length > 0
            ? readyDocuments.map((a) => ({ id: a.id, mime: a.mime, name: a.name }))
            : undefined,
      },
      { id: crypto.randomUUID(), role: 'assistant', ...emptyAssistant() },
    ])

    const controller = new AbortController()
    abortRef.current = controller
    const adoptSession = (id: string) => {
      if (sessionRef.current === id) return
      sessionRef.current = id
      adoptedRef.current = id
      // Same route pattern serves basePath and basePath/:id, so this
      // only re-renders: the live stream keeps its component state.
      navigate(`${basePath}/${id}`, { replace: true })
    }
    let handedOffToLive = false
    try {
      await chatStream(
        {
          session_id: sessionRef.current,
          message,
          agent: agentName,
          route: routeName || undefined,
          skill_hint: hint,
          attachments:
            ready.length > 0 ? ready.map((a) => ({ id: a.id, name: a.name })) : undefined,
          knowledge: sentKnowledge.length > 0 ? sentKnowledge : undefined,
          references:
            sentReferences.length > 0
              ? sentReferences.map((r) => ({ kind: r.kind, id: r.id }))
              : undefined,
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
        if (err.status === 401 || err.status === 503) {
          onNeedToken()
          updateLast((m) => ({ ...m, streaming: false, error: errorText(err) }))
        } else if (err.status === 409 && err.code === 'turn_in_flight') {
          // Lost the race to a turn already running elsewhere (another
          // tab, a retry): not a failure, just attach to it like a
          // page reload mid-turn would. attachLive owns streaming/abort
          // state from here, so the finally below must not touch it.
          toast.info('A reply is already in progress, reattaching')
          handedOffToLive = true
          attachLive(sessionRef.current!)
        } else updateLast((m) => ({ ...m, streaming: false, error: err.message }))
      } else if (sessionRef.current) {
        // A transport-level failure (dropped connection, proxy timeout,
        // laptop sleep) throws a plain error, not ChatError: the turn
        // itself runs detached from this request (D-042), so it's very
        // likely still alive server-side. Reattach via /live instead of
        // surfacing a false "failed" state; attachLive's own error path
        // falls back to a transcript refetch if the turn is in fact gone.
        toast.info('Connection dropped, reattaching')
        handedOffToLive = true
        attachLive(sessionRef.current)
      } else {
        updateLast((m) => ({ ...m, streaming: false, error: String(err) }))
      }
    } finally {
      if (!handedOffToLive) {
        abortRef.current = null
        if (!controller.signal.aborted) {
          setStreaming(false)
          updateLast((m) => ({ ...m, streaming: false }))
        }
      }
    }
  }

  const send = () =>
    void sendMessage(draft, agent, skillHint, route, attachments, knowledge, references)

  // stop asks the server to cancel the in-flight turn (chat.Service now
  // runs it detached from this request, so abortRef.current?.abort()
  // alone only stops this tab's rendering, never the turn itself: see
  // stopTurn's doc comment). Fire-and-forget: the turn's abnormal-end
  // persistence and the local abort together already leave the UI in a
  // sane state regardless of whether this call lands.
  const stop = () => {
    const sessionId = sessionRef.current
    abortRef.current?.abort()
    if (sessionId) stopTurn(sessionId).catch(() => toast.error('Could not stop the reply'))
  }

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
    setPin(true)
    setItems((prev) => {
      const next = [...prev]
      const last = next[next.length - 1]
      if (last?.role === 'assistant') next[next.length - 1] = { ...emptyAssistant(), id: last.id, role: 'assistant' }
      else next.push({ id: crypto.randomUUID(), role: 'assistant', ...emptyAssistant() })
      return next
    })

    const controller = new AbortController()
    abortRef.current = controller
    let handedOffToLive = false
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
        if (err.status === 401 || err.status === 503) {
          onNeedToken()
          updateLast((m) => ({ ...m, streaming: false, error: errorText(err) }))
        } else if (err.status === 409 && err.code === 'turn_in_flight') {
          // Same handoff as sendMessage: a turn is already running
          // (another tab's send/retry won the race): attach to it
          // instead of showing a failure. attachLive owns
          // streaming/abort state from here.
          toast.info('A reply is already in progress, reattaching')
          handedOffToLive = true
          attachLive(sessionId)
        } else updateLast((m) => ({ ...m, streaming: false, error: err.message }))
      } else {
        // A transport-level failure (dropped connection, proxy timeout,
        // laptop sleep) throws a plain error, not ChatError: the turn
        // itself runs detached from this request (D-042), so it's very
        // likely still alive server-side. Reattach via /live instead of
        // surfacing a false "failed" state.
        toast.info('Connection dropped, reattaching')
        handedOffToLive = true
        attachLive(sessionId)
      }
    } finally {
      if (!handedOffToLive) {
        abortRef.current = null
        if (!controller.signal.aborted) {
          setStreaming(false)
          updateLast((m) => ({ ...m, streaming: false }))
        }
      }
    }
  }

  // Consume a home-screen intent exactly once: `send` fires the
  // message immediately, `draft` prefills the composer, `skillHint`
  // pins the chip. sendMessage takes hint explicitly here rather than
  // relying on the skillHint state landing before the call: setState
  // doesn't take effect until the next render.
  useEffect(() => {
    if (lockedSkillHint || intentConsumedRef.current) return
    const intent = location.state as ChatIntent | null
    if (
      !intent ||
      (!intent.send &&
        !intent.draft &&
        !intent.agent &&
        !intent.route &&
        !intent.skillHint &&
        !intent.attachments &&
        !intent.knowledge)
    )
      return
    intentConsumedRef.current = true
    window.history.replaceState(null, '') // don't refire on back/refresh
    const who = intent.agent ?? agent
    const whichRoute = intent.route ?? route
    if (intent.agent) pickAgent(intent.agent)
    if (intent.route) pickRoute(intent.route)
    if (intent.skillHint) setSkillHint(intent.skillHint)
    if (intent.knowledge) setKnowledge(intent.knowledge)
    if (intent.send || intent.attachments)
      // sendMessage takes knowledge explicitly here for the same reason
      // as hint/whichRoute above: the setKnowledge call just above hasn't
      // landed in this render, so `knowledge` state is still stale.
      void sendMessage(
        intent.send ?? '',
        who,
        intent.skillHint,
        whichRoute,
        intent.attachments,
        intent.knowledge ?? knowledge,
      )
    else if (intent.draft) setDraft(intent.draft)
    // Mount-time only by design: the intent rides the navigation that
    // created this page instance; the ref guards strict-mode replays.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // The status line and the inline/modal approval cards all show the
  // oldest unanswered prompt of the live turn; answering posts the
  // decision and drops it locally.
  const last = items[items.length - 1]
  const liveAssistant = last?.role === 'assistant' && last.streaming ? last : null
  const pendingPermission = last?.role === 'assistant' ? last.permissions[0] : undefined
  const decide = (id: string, decision: 'once' | 'session' | 'deny') => {
    updateLast((m) => clearPermission(m, id))
    setShowApprovalDialog(false)
    answerPermission(id, decision).catch((err: unknown) => {
      // A replayed ask whose turn died (brain restart) answers 404
      // "unknown or already-answered": the prompt is already gone
      // locally (above), so just tell the user why nothing happened.
      // Any other error is the same 10-minute-timeout-resolves-as-deny
      // case as before: nothing useful to render.
      if (err instanceof ChatError && err.status === 404) {
        toast.error('This request is no longer waiting for a response')
      }
    })
  }

  // Present the modal ApprovalDialog only when a request arrives while
  // the transcript is scrolled away from it (contract 14.7): a pinned
  // view already shows the inline card in place, no modal needed.
  const pendingPermissionId = pendingPermission?.id
  useEffect(() => {
    if (pendingPermissionId && !pinnedRef.current) setShowApprovalDialog(true)
    if (!pendingPermissionId) setShowApprovalDialog(false)
  }, [pendingPermissionId])

  // The status line's "done" summary shows for 5s after a turn
  // finishes, then reverts to idle (contract 14.3). A new turn
  // starting before the 5s elapses clears it immediately so the line
  // doesn't lag showing "done" while the next one is already working.
  useEffect(() => {
    if (streaming) {
      setJustDone(null)
      return
    }
    const finished = last?.role === 'assistant' && !last.streaming
    if (!finished) return
    setJustDone({ durationMs: last.meta?.durationMs, toolCount: last.tools.length })
    window.clearTimeout(doneTimerRef.current)
    doneTimerRef.current = window.setTimeout(() => setJustDone(null), 5000)
    return () => window.clearTimeout(doneTimerRef.current)
    // Only re-evaluate when streaming stops or the trailing item's id
    // changes (a new turn finished): re-running on every items update
    // would restart the 5s timer while a later message merely renders.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [streaming, last?.id])

  const statusPhase = (() => {
    if (justDone) return { kind: 'done' as const, ...justDone }
    return agentPhaseFromState(liveAssistant, { pendingPermission: Boolean(pendingPermission) })
  })()

  const focusApprovalCard = () => {
    const id = pendingPermission?.id
    if (!id) return
    const el = document.getElementById(`approval-${id}`)
    el?.scrollIntoView({ block: 'center' })
    el?.focus()
  }

  // Re-derived from `items` every render (not captured on open) so the
  // panel keeps showing live tool/reasoning state while its turn streams.
  const isEmpty = items.length === 0 && !loadError
  const activityItem = items.find((i) => i.id === activityId)
  const activityMsg = activityItem?.role === 'assistant' ? activityItem : undefined

  return (
    <TooltipProvider>
      <div
        className={`flex h-full min-h-0 min-w-0 flex-col ${isEmpty ? 'justify-center' : ''}`}
      >
        {pendingPermission && (
          <ApprovalDialog
            request={pendingPermission}
            onDecision={decide}
            open={showApprovalDialog}
            onOpenChange={setShowApprovalDialog}
          />
        )}
        <Sheet open={activityMsg !== undefined} onOpenChange={(open) => !open && setActivityId(null)}>
          {activityMsg && <ActivityPanel msg={activityMsg} />}
        </Sheet>

        {isEmpty ? (
          <div className="flex flex-col items-center justify-center">
            <p className="text-base text-muted-foreground">{emptyHeading}</p>
            {emptySubtext && emptySubtext !== emptyHeading && (
              <p className="mt-1 text-sm text-muted-foreground">{emptySubtext}</p>
            )}
          </div>
        ) : (
          <div ref={listRef} onScroll={trackPin} className="flex-1 overflow-y-auto overflow-x-hidden">
            <div className="relative mx-auto w-full min-w-0 space-y-8 pt-8 pb-6">
              {loadError && (
                <Alert tone="destructive">
                  <AlertTitle>Could not load this conversation</AlertTitle>
                  <p className="font-mono text-xs">{loadError}</p>
                </Alert>
              )}
              {items.map((item, i) => {
                switch (item.role) {
                  case 'user':
                    return (
                      <UserMessage
                        key={item.id}
                        text={item.text}
                        images={item.images}
                        documents={item.documents}
                        localUrls={localUrlsRef.current.get(item.id)}
                        // A trailing user message means the turn died before any
                        // assistant event reached the transcript: retry re-runs
                        // it server-side, same trailing-only condition as above.
                        onRetry={i === items.length - 1 && !streaming ? retryLast : undefined}
                      />
                    )
                  case 'compaction':
                    return <CompactionDivider key={item.id} text={item.text} />
                  case 'interrupted':
                    return <InterruptedMessage key={item.id} text={item.text} />
                  case 'error':
                    return (
                      <ErrorMessage
                        key={item.id}
                        text={item.text}
                        // Same trailing-only condition as user/assistant items:
                        // a failed turn had no way back into the UI at all
                        // without this: the user had to type the message again.
                        onRetry={i === items.length - 1 && !streaming ? retryLast : undefined}
                      />
                    )
                  default:
                    return (
                      <AssistantMessage
                        key={item.id}
                        msg={item}
                        // Retry only ever targets the trailing dangling turn
                        // (the session's last event server-side): never a
                        // mid-transcript message.
                        onRetry={i === items.length - 1 && !streaming ? retryLast : undefined}
                        onShowActivity={() => setActivityId(item.id)}
                        onDecision={decide}
                      />
                    )
                }
              })}
              <div ref={bottomRef} />
            </div>
            {!pinned && streaming && (
              <div className="sticky bottom-2 mx-auto flex w-fit justify-center">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    setPin(true)
                    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
                  }}
                >
                  <ArrowDown aria-hidden />
                  Jump to latest
                </Button>
              </div>
            )}
          </div>
        )}

        <div className="mx-auto w-full pb-6">
          <AgentStatusLine
            phase={
              statusPhase.kind === 'waiting'
                ? { ...statusPhase, onFocusRequest: focusApprovalCard }
                : statusPhase
            }
            className="mb-2"
          />
          <form
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
              streaming={streaming}
              onStop={stop}
              placeholder={placeholder}
              attachments={attachments}
              onAttachments={setAttachments}
              knowledge={knowledge}
              onKnowledge={updateKnowledge}
              agentKnowledge={agentKnowledge}
              references={references}
              onReferences={setReferences}
            />
            <p className="mt-2 text-center text-xs text-muted-foreground">
              <Kbd>Enter</Kbd> to send · Shift+Enter for a new line
            </p>
          </form>
        </div>
      </div>
    </TooltipProvider>
  )
}
