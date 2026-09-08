import { FileText, GitFork, GitPullRequest, MessageSquare, Trash2, Upload } from 'lucide-react'
import { Children, Fragment, useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import {
  answerMissionPermission,
  answerMissionQuestion,
  approveMissionPlan,
  cancelMission,
  deleteMission,
  getMission,
  listDestinations,
  listSchedules,
  missionEvents,
  missionUsage,
  openMissionPR,
  pushMission,
  rediscoverMission,
  replanMission,
  resumeMission,
  sendMissionNote,
} from '../api/client'
import type {
  Destination,
  ExecutorProgressPayload,
  GitHubDestinationConfig,
  Mission,
  MissionEvent,
  MissionPROpenedPayload,
  MissionUsage,
  Schedule,
} from '../api/types'
import { ArtifactsSection } from '../components/missions/ArtifactsSection'
import { CostDisplay } from '../components/missions/CostDisplay'
import { DiscoverSection } from '../components/missions/DiscoverSection'
import { FindingsSection } from '../components/missions/FindingsSection'
import { GoalSection } from '../components/missions/GoalSection'
import { HarnessIcon, harnessLabel } from '../components/missions/HarnessIcon'
import { InputRequestGate } from '../components/missions/InputRequestGate'
import { MarkdownField } from '../components/missions/MarkdownField'
import { MissionPermissionGate } from '../components/missions/MissionPermissionGate'
import { failedPhaseFromEvents, PhaseStepper } from '../components/missions/PhaseStepper'
import { PlanApprovalGate } from '../components/missions/PlanApprovalGate'
import { PlanSection } from '../components/missions/PlanSection'
import { ResultSection } from '../components/missions/ResultSection'
import { ReviewRoutePicker } from '../components/missions/ReviewRoutePicker'
import { TimelineSection } from '../components/missions/TimelineSection'
import { ModelBadge } from '../components/ModelBadge'
import { envIcon } from '../components/icons/EnvIcons'
import { Alert, AlertDescription, AlertTitle } from '../components/ui/alert'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../components/ui/dialog'
import { errText } from '../lib/errors'
import { ConfirmDialog } from '../components/timothy/confirm-dialog'
import { CopyButton } from '../components/timothy/copy-button'
import { PageHeader } from '../components/timothy/page-header'
import { PageShell } from '../components/timothy/page-shell'
import { Panel } from '../components/timothy/panel'
import { StatusBadge } from '../components/timothy/status-badge'
import { missionStatus } from '../components/timothy/status'
import { describeCron } from '../lib/schedules'
import { playAlertSound } from '../lib/alertSound'
import { subscribeEvents } from '../lib/events'
import { compact, euDateTime, formatDuration, missionDisplayName, money, relativeTime } from '../lib/format'

// MetaLine renders the mission metadata as one dot-separated row; falsy
// children are dropped so separators never double up. It heads the
// usage panel, so the badge grid below it gets a rule.
function MetaLine({ children }: { children: ReactNode }) {
  const items = Children.toArray(children).filter(Boolean)
  return (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-border pb-3 text-xs text-muted-foreground">
      {items.map((item, i) => (
        <Fragment key={i}>
          {i > 0 && (
            <span aria-hidden className="select-none">
              ·
            </span>
          )}
          {item}
        </Fragment>
      ))}
    </div>
  )
}

// unbilledTooltipLine formats the billed cost pill's tooltip line, in
// that pill's OWN currency only — prefers the currency-converted
// unbilled amount (matching the pill's display currency), falling back
// to the raw amount in that same currency if no converted figure
// exists for it (D-013: never guess a rate, but never hide the figure
// either). null when neither map has an entry for currency, which also
// covers the all-billed mission with no unbilled spend at all.
function unbilledTooltipLine(usage: MissionUsage, currency: string): string | null {
  const converted = usage.converted_unbilled_cost_by_currency?.[currency]
  const raw = usage.unbilled_cost_by_currency?.[currency]
  const amount = converted ?? raw
  if (amount == null) return null
  return `+${money(amount, currency)} unbilled`
}

// budgetPercentSpent figures the "% of budget" badge value in the
// mission's own budget currency — prefers the currency-converted spend
// figure (matching how the cost pills already display), falling back
// to the raw same-currency amount. Returns null when spend in the
// budget currency can't be stated at all (no converted figure and the
// raw bucket is zero while spend exists in other currencies) — showing
// 0% there would be dishonest, not just imprecise.
function budgetPercentSpent(usage: MissionUsage, budgetCurrency: string, budgetAmount: number): number | null {
  const converted = usage.converted_cost_by_currency?.[budgetCurrency]
  const raw = usage.cost_by_currency[budgetCurrency]
  const amount = converted ?? raw
  const hasOtherSpend = Object.entries(usage.cost_by_currency).some(
    ([currency, cost]) => currency !== budgetCurrency && cost > 0,
  )
  if ((amount == null || amount === 0) && hasOtherSpend) return null
  return Math.round(((amount ?? 0) / budgetAmount) * 100)
}

function formatDate(v?: string): string {
  if (!v) return 'N/A'
  return new Date(v).toLocaleString()
}

// githubFullName/githubHTMLURL derive the display label and browsable
// link straight from repo_url's https clone URL — the mission row only
// stores the clone URL, never a separate html_url.
function githubFullName(repoURL: string): string {
  return repoURL.replace(/^https?:\/\/[^/]+\//, '').replace(/\.git$/, '')
}

function githubHTMLURL(repoURL: string): string {
  return repoURL.replace(/\.git$/, '')
}

// turnStats derives the detail view's Turns/Processing figures purely
// from the mission.turn events the page already fetches — one event
// per phase run (driver.go's Advance), so counting them is an honest
// turn count without a dedicated backend field.
function turnStats(events: MissionEvent[]): { turns: number; processingMs: number } {
  let turns = 0
  let processingMs = 0
  for (const e of events) {
    if (e.kind !== 'mission.turn') continue
    turns++
    const payload = e.payload
    if (payload && typeof payload === 'object' && 'duration_ms' in payload) {
      const ms = (payload as { duration_ms?: unknown }).duration_ms
      if (typeof ms === 'number') processingMs += ms
    }
  }
  return { turns, processingMs }
}

const resumableStatuses = new Set(['paused', 'waiting_for_input'])
const terminalPhases = new Set(['done', 'failed'])

// pauseReasonLabels renders the harness's PauseReason values
// (statemachine.go) readably; an unknown value falls back to itself.
const pauseReasonLabels: Record<string, string> = {
  backoff: 'repeated worker failures',
  no_progress: 'no progress on open findings',
  infra: 'infrastructure error',
  budget: 'budget exhausted',
  mixed_currency: 'spend in an unconvertible currency',
  approval: 'plan awaiting approval',
  review_exhausted: 'review rounds exhausted, findings still open',
}

// runsPlanless mirrors missions.Mission.RunsPlanless (D-090, issue
// #459): light and flow=discover_generate both run generate with no
// plan, no review; the worker's final message is the deliverable
// (final_output), not last_evidence.
function runsPlanless(mission: Mission): boolean {
  return !!mission.light || mission.flow === 'discover_generate'
}

// latestPROpened finds the most recent mission.pr_opened event so the
// PR chip persists across reloads — the timeline is the durable record
// of a PR having been opened, the immediate POST response is only the
// optimistic first paint.
function latestPROpened(events: MissionEvent[]): MissionPROpenedPayload | null {
  for (let i = events.length - 1; i >= 0; i--) {
    if (events[i].kind === 'mission.pr_opened') {
      return events[i].payload as MissionPROpenedPayload
    }
  }
  return null
}

// latestExecutorProgress finds the most recent executor.progress event
// so the phase header can show a lightweight live indicator — these
// events fire on every byte the delegated CLI executor writes, far too
// often to render as individual Timeline rows (TimelineSection drops
// them), so only the latest one is surfaced here.
function latestExecutorProgress(events: MissionEvent[]): ExecutorProgressPayload | null {
  for (let i = events.length - 1; i >= 0; i--) {
    if (events[i].kind === 'executor.progress') {
      return events[i].payload as ExecutorProgressPayload
    }
  }
  return null
}

// latestExecutorSpawn finds the most recent executor.spawned event, so
// the stats row can name the delegated CLI that actually ran the work
// — unlike the native session's model pill, this is a fact about what
// ran and stays shown once the mission is terminal.
function latestExecutorSpawn(
  events: MissionEvent[],
): { harness: string; provider: string; model: string } | null {
  for (let i = events.length - 1; i >= 0; i--) {
    if (events[i].kind === 'executor.spawned') {
      const { harness, provider, model } = events[i].payload as {
        harness: string
        provider: string
        model: string
      }
      return { harness, provider, model }
    }
  }
  return null
}

// statusLabel mirrors MissionCard's own rule for the header badge:
// non-terminal statuses print as-is (underscores to spaces), phase=done
// reads as "done", and phase=failed reads as "cancelled" (failure_reason)
// or "failed".
function statusLabel(mission: Mission): string {
  if (mission.phase === 'done') return 'done'
  if (mission.phase === 'failed') return mission.failure_reason === 'cancelled' ? 'cancelled' : 'failed'
  return mission.status.replace(/_/g, ' ')
}

export function MissionDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [mission, setMission] = useState<Mission | null>(null)
  const [events, setEvents] = useState<MissionEvent[]>([])
  const [usage, setUsage] = useState<MissionUsage | null>(null)
  const [schedule, setSchedule] = useState<Schedule | null>(null)
  // destinations backs the badges/Destinations card below: fetched
  // once, same call the mission create form uses, so a destination
  // entry's own id-only reference can resolve to its name/kind/config.
  const [destinations, setDestinations] = useState<Destination[] | null>(null)
  useEffect(() => {
    listDestinations()
      .then(setDestinations)
      .catch(() => {
        // Non-fatal: badges/card just fall back to id-only rendering.
      })
  }, [])
  const [busy, setBusy] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [intervening, setIntervening] = useState(false)
  const [noteText, setNoteText] = useState('')
  const [sendingNote, setSendingNote] = useState(false)
  const [pushing, setPushing] = useState(false)
  const [openingPR, setOpeningPR] = useState(false)
  // prInfo is the immediate response from a successful "Push & open PR"
  // click — shown right away, before the mission.pr_opened event has
  // necessarily made it into the fetched timeline (refresh() below is
  // fire-and-forget). latestPROpened(events) is the durable source once
  // the timeline catches up or the page reloads.
  const [prInfo, setPRInfo] = useState<MissionPROpenedPayload | null>(null)
  // answeredPermission tracks the pending_permission id the user just
  // decided on, so the card stops being actionable immediately — the
  // decision POST resolves the broker right away, but
  // mission.pending_permission on the mission row only clears once the
  // approved tool call finishes executing (runner.go clears it on the
  // tool result), which can be minutes for a long-running command.
  // Keyed by the permission id string (not a bare boolean) so a NEW
  // pending_permission arriving later — a different id — renders as a
  // fresh actionable card rather than staying stuck in the answered
  // state from the previous one.
  const [answeredPermission, setAnsweredPermission] = useState<{
    id: string
    decision: 'once' | 'session' | 'deny'
  } | null>(null)
  // answeredPlanDecision mirrors answeredPermission's own pattern: the
  // decision POST resolves right away, but the mission's phase/
  // pause_reason only clear once the harness has acted on it, so the
  // card must stop being actionable before that refetch lands. Reset
  // to null whenever the mission leaves the parked-on-approval state
  // (a fresh park re-renders actionable).
  const [answeredPlanDecision, setAnsweredPlanDecision] = useState<'approve' | 'replan' | 'rediscover' | null>(
    null,
  )
  // answeredQuestion mirrors answeredPermission's own pattern: the
  // answer POST resolves right away, but pending_input only clears once
  // the harness resumes the mission and the next fetch catches up.
  // Keyed by the question text (not a bare boolean) so a NEW question
  // arriving later renders as a fresh actionable card.
  const [answeredQuestion, setAnsweredQuestion] = useState<{ question: string; answer: string } | null>(
    null,
  )

  const refreshSeq = useRef(0)

  const refresh = useCallback(() => {
    if (!id) return
    const seq = ++refreshSeq.current
    getMission(id).then((m) => {
      if (seq === refreshSeq.current) setMission(m)
    }, () => undefined)
    missionEvents(id).then((e) => {
      if (seq === refreshSeq.current) setEvents(e)
    }, () => undefined)
    missionUsage(id).then((u) => {
      if (seq === refreshSeq.current) setUsage(u)
    }, () => undefined)
  }, [id])

  const pendingPermission = mission?.pending_permission
  const wasPendingRef = useRef(false)
  const pendingPlanApproval = mission?.phase === 'plan' && mission?.pause_reason === 'approval'
  const wasPlanParkedRef = useRef(false)
  const pendingInput = mission?.pending_input
  const wasQuestionPendingRef = useRef(false)

  useEffect(() => {
    refresh()
    // Only refetch for a signal naming THIS mission — other missions'
    // signals don't concern this page. onReady covers the initial
    // connect and every reconnect, catching anything missed while
    // disconnected.
    return subscribeEvents(
      (sig) => {
        if (sig.kind === 'mission' && sig.id === id) refresh()
      },
      () => refresh(),
    )
  }, [refresh, id])

  // No GET-by-id for schedules; the list is small, so find the one
  // this mission fired from rather than adding a single-row endpoint.
  const scheduleID = mission?.schedule_id
  useEffect(() => {
    if (!scheduleID) {
      setSchedule(null)
      return
    }
    listSchedules().then(
      (rows) => setSchedule(rows.find((s) => s.id === scheduleID) ?? null),
      () => undefined,
    )
  }, [scheduleID])

  // Chime only on the transition into a permission block, not on every
  // poll while it stays pending — the banner itself is the persistent
  // visual cue.
  useEffect(() => {
    if (pendingPermission && !wasPendingRef.current) playAlertSound()
    wasPendingRef.current = !!pendingPermission
  }, [pendingPermission])

  // Same chime-once-on-transition treatment for entering the
  // parked-on-plan-approval state, tracked with its own ref so it
  // doesn't conflate with the permission chime above.
  useEffect(() => {
    if (pendingPlanApproval && !wasPlanParkedRef.current) playAlertSound()
    wasPlanParkedRef.current = pendingPlanApproval
    if (!pendingPlanApproval) setAnsweredPlanDecision(null)
  }, [pendingPlanApproval])

  // Same chime-once-on-transition treatment for a new ask_user question
  // arriving, tracked with its own ref.
  useEffect(() => {
    if (pendingInput && !wasQuestionPendingRef.current) playAlertSound()
    wasQuestionPendingRef.current = !!pendingInput
  }, [pendingInput])

  if (!id) return null
  if (!mission) {
    return (
      <PageShell>
        <p className="text-sm text-muted-foreground">Loading…</p>
      </PageShell>
    )
  }

  const canResume = resumableStatuses.has(mission.status)
  const canCancel = !terminalPhases.has(mission.phase)
  const isTerminal = terminalPhases.has(mission.phase)
  // isGitHubConnection: a coding mission cloned through a connector —
  // gets the two-button push affordance; every other mission keeps the
  // existing single push flow (currently: none rendered — see slice 3
  // notes). prChip prefers the optimistic click response over the
  // timeline so it appears the instant the PR is opened, falling back
  // to the durable mission.pr_opened event on reload/cross-tab.
  const isGitHubConnection = !!mission.connector_id
  const prChip = prInfo ?? latestPROpened(events)

  // destinationsByID resolves a destination entry's destination_id to
  // its saved row (name/kind/config): a github row's config.mode drives
  // the auto-push/auto-PR badges and the Destinations card's mode text.
  const destinationsByID = new Map((destinations ?? []).map((d) => [d.id, d]))
  const githubModes = (mission.destinations ?? [])
    .map((d) => (d.destination_id ? destinationsByID.get(d.destination_id) : undefined))
    .filter((d): d is Destination => !!d && d.kind === 'github')
    .map((d) => (d.config as unknown as GitHubDestinationConfig).mode)

  const { turns, processingMs } = turnStats(events)
  const executorActivity = isTerminal ? null : latestExecutorProgress(events)
  const executorSpawn = latestExecutorSpawn(events)
  // A live mission's elapsed span runs to now, not its last updated_at
  // (which only moves on a state transition, not while a turn is
  // in-flight) — otherwise "Elapsed" would understate a mission stuck
  // mid-turn.
  const elapsedEnd = isTerminal ? mission.updated_at : new Date().toISOString()
  const elapsedMs = new Date(elapsedEnd).getTime() - new Date(mission.created_at).getTime()
  const costEntries: [string, number][] =
    usage && usage.requests > 0
      ? usage.converted_cost_by_currency && Object.keys(usage.converted_cost_by_currency).length > 0
        ? Object.entries(usage.converted_cost_by_currency)
        : Object.entries(usage.cost_by_currency)
      : []

  // pause_message never carries real content (the state machine clears
  // it on every transition — see store.go's ApplyTransition comment);
  // the actual detail only lives in the mission.paused event itself,
  // so pull it from the most recent one while still paused.
  const pauseDetail =
    mission.status === 'paused'
      ? (() => {
          for (let i = events.length - 1; i >= 0; i--) {
            if (events[i].kind === 'mission.paused') {
              const payload = events[i].payload
              if (payload && typeof payload === 'object' && 'detail' in payload) {
                const detail = (payload as { detail?: unknown }).detail
                return typeof detail === 'string' ? detail : undefined
              }
              return undefined
            }
          }
          return undefined
        })()
      : undefined

  const resume = async () => {
    setBusy(true)
    try {
      await resumeMission(id)
      toast.success('Mission resumed')
      refresh()
    } catch (err) {
      toast.error('Could not resume mission', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  const sendNote = async () => {
    const text = noteText.trim()
    if (!text) return
    setSendingNote(true)
    try {
      await sendMissionNote(id, text)
      setNoteText('')
      setIntervening(false)
      toast.success('Note sent')
    } catch (err) {
      toast.error('Could not send note', { description: errText(err) })
    } finally {
      setSendingNote(false)
    }
  }

  const cancel = async () => {
    setBusy(true)
    try {
      await cancelMission(id)
      toast.success('Mission cancelled')
      refresh()
    } catch (err) {
      toast.error('Could not cancel mission', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  const push = async () => {
    setPushing(true)
    try {
      const { branch, remote_host } = await pushMission(id)
      toast.success(`Pushed ${branch} to ${remote_host}`)
      refresh()
    } catch (err) {
      toast.error('Could not push branch', { description: errText(err) })
    } finally {
      setPushing(false)
    }
  }

  const openPR = async () => {
    setOpeningPR(true)
    try {
      const pr = await openMissionPR(id)
      setPRInfo(pr)
      toast.success(`Pull request #${pr.number} opened`)
      refresh()
    } catch (err) {
      toast.error('Could not open pull request', { description: errText(err) })
    } finally {
      setOpeningPR(false)
    }
  }

  const remove = async () => {
    setBusy(true)
    try {
      await deleteMission(id)
      toast.success('Mission deleted')
      navigate('/missions')
    } catch (err) {
      toast.error('Could not delete mission', { description: errText(err) })
      setBusy(false)
    }
  }

  const decidePermission = async (decision: 'once' | 'session' | 'deny') => {
    const permissionID = pendingPermission
    try {
      await answerMissionPermission(id, decision)
      if (permissionID) setAnsweredPermission({ id: permissionID, decision })
    } catch (err) {
      toast.error('Could not answer permission request', { description: errText(err) })
    } finally {
      refresh()
    }
  }

  const approvePlan = async () => {
    try {
      await approveMissionPlan(id)
      setAnsweredPlanDecision('approve')
    } catch (err) {
      toast.error('Could not approve plan', { description: errText(err) })
    } finally {
      refresh()
    }
  }

  const requestReplan = async (feedback: string) => {
    try {
      await replanMission(id, feedback || undefined)
      setAnsweredPlanDecision('replan')
    } catch (err) {
      toast.error('Could not request a replan', { description: errText(err) })
    } finally {
      refresh()
    }
  }

  const rediscover = async () => {
    try {
      await rediscoverMission(id)
      setAnsweredPlanDecision('rediscover')
    } catch (err) {
      toast.error('Could not send mission back to discover', { description: errText(err) })
    } finally {
      refresh()
    }
  }

  const answerQuestion = async (answer: string) => {
    const question = pendingInput?.question
    try {
      await answerMissionQuestion(id, answer)
      if (question) setAnsweredQuestion({ question, answer })
    } catch (err) {
      toast.error('Could not send answer', { description: errText(err) })
    } finally {
      refresh()
    }
  }

  // Cross-tab/refresh fallback: this tab may not have the optimistic
  // answeredPermission state (a fresh load, or the decision happened
  // in another tab) but the events list already fetched can still show
  // the answer — the latest permission_answered event arriving after
  // the latest permission_requested event means the currently pending
  // id has, in fact, already been decided.
  const answeredFromEvents = (() => {
    if (!pendingPermission) return false
    let lastRequestedSeq = -1
    let lastAnsweredSeq = -1
    for (const e of events) {
      if (e.kind === 'mission.permission_requested') lastRequestedSeq = e.seq
      if (e.kind === 'mission.permission_answered') lastAnsweredSeq = e.seq
    }
    return lastRequestedSeq >= 0 && lastAnsweredSeq > lastRequestedSeq
  })()

  const answeredDecision: 'once' | 'session' | 'deny' | 'unknown' | undefined =
    answeredPermission && answeredPermission.id === pendingPermission
      ? answeredPermission.decision
      : answeredFromEvents
        ? 'unknown'
        : undefined

  return (
    <PageShell>
      <PageHeader
        breadcrumbs={[{ label: 'Missions', href: '/missions' }, { label: missionDisplayName(mission) }]}
        title={missionDisplayName(mission)}
        meta={<StatusBadge status={missionStatus(mission)} label={statusLabel(mission)} />}
        actions={
          <>
            {isGitHubConnection && mission.branch && (
              <>
                <Button variant="outline" size="icon" aria-label="Push branch" disabled={pushing} onClick={() => void push()}>
                  <Upload aria-hidden />
                </Button>
                {!prChip && (
                  <Button
                    variant="outline"
                    size="icon"
                    aria-label="Push & open PR"
                    disabled={openingPR}
                    onClick={() => void openPR()}
                  >
                    <GitPullRequest aria-hidden />
                  </Button>
                )}
              </>
            )}
            {canResume && (
              <Button disabled={busy} onClick={() => void resume()}>
                Resume
              </Button>
            )}
            <Button variant="outline" disabled={isTerminal} onClick={() => setIntervening(true)}>
              <MessageSquare aria-hidden />
              Intervene
            </Button>
            {canCancel && (
              <Button variant="destructive" disabled={busy} onClick={() => void cancel()}>
                Cancel
              </Button>
            )}
            {isTerminal && (
              <Button variant="ghost" size="icon" aria-label="Fork" onClick={() => navigate(`/missions/new?parent=${mission.id}`)}>
                <GitFork aria-hidden />
              </Button>
            )}
            {isTerminal && (
              <Button
                variant="destructive"
                size="icon"
                aria-label="Delete mission"
                disabled={busy}
                onClick={() => setConfirmDelete(true)}
              >
                <Trash2 aria-hidden />
              </Button>
            )}
          </>
        }
      >
        <PhaseStepper
          phase={mission.phase}
          light={mission.light}
          failedAt={mission.phase === 'failed' ? failedPhaseFromEvents(events, mission.light) : undefined}
        />
      </PageHeader>

      {schedule && (
        <p className="mt-1 text-xs text-muted-foreground">
          Recurring · {describeCron(schedule.cron)} · next run {formatDate(schedule.next_run)}
        </p>
      )}
      {mission.parent_mission_id && (
        <p className="mt-1 text-xs text-muted-foreground">
          Follow-up of{' '}
          <Link to={`/missions/${mission.parent_mission_id}`} className="underline underline-offset-2 hover:text-foreground">
            {mission.parent_mission_id.slice(0, 8)}
          </Link>
        </p>
      )}
      {mission.attachments && mission.attachments.length > 0 && (
        <div className="mt-1 flex flex-wrap gap-1.5">
          {mission.attachments.map((a) => (
            <Badge key={a.id} variant="outline" size="sm">
              <FileText aria-hidden />
              {a.name ?? a.id.slice(0, 8)}
            </Badge>
          ))}
        </div>
      )}

      {mission.status === 'paused' && mission.pause_reason && (
        <Alert tone="warning" className="mt-4">
          <AlertTitle>Paused: {pauseReasonLabels[mission.pause_reason] ?? mission.pause_reason}</AlertTitle>
          {pauseDetail && <AlertDescription>{pauseDetail}</AlertDescription>}
        </Alert>
      )}

      <div className="mt-6 space-y-4">
        {mission.pending_permission && (
          <MissionPermissionGate
            tool={mission.pending_permission_tool}
            args={mission.pending_permission_args}
            danger={mission.pending_permission_danger}
            rationale={mission.pending_permission_rationale}
            answeredDecision={answeredDecision}
            onDecide={(d) => void decidePermission(d)}
            timeoutSeconds={mission.permission_timeout_seconds}
          />
        )}

        {pendingPlanApproval && (
          <PlanApprovalGate
            units={mission.plan?.units ?? []}
            assumptions={mission.plan?.assumptions}
            answeredDecision={answeredPlanDecision ?? undefined}
            onApprove={() => void approvePlan()}
            onReplan={(feedback) => void requestReplan(feedback)}
            onRediscover={() => void rediscover()}
          />
        )}

        {pendingInput && (
          <InputRequestGate
            question={pendingInput.question}
            kind={pendingInput.kind}
            options={pendingInput.options}
            proposedDefault={pendingInput.proposed_default}
            answered={
              answeredQuestion && answeredQuestion.question === pendingInput.question
                ? answeredQuestion.answer
                : undefined
            }
            onAnswer={(answer) => void answerQuestion(answer)}
            askedAt={pendingInput.asked_at}
          />
        )}
      </div>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title="Delete this mission?"
        description="This removes the mission, its events, and its workspace. This cannot be undone."
        confirmLabel="Delete"
        destructive
        loading={busy}
        onConfirm={() => void remove()}
      />

      <Dialog open={intervening} onOpenChange={setIntervening}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Intervene</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            Currently in <span className="font-medium text-foreground">{mission.phase}</span> ·{' '}
            {mission.status.replace(/_/g, ' ')}
          </p>
          {mission.status === 'paused' && (
            <ReviewRoutePicker
              mission={mission}
              onSaved={() => {
                setIntervening(false)
                refresh()
              }}
            />
          )}
          <MarkdownField
            placeholder="Steer this mission (markdown supported)…"
            value={noteText}
            onChange={setNoteText}
            disabled={sendingNote}
            rows={5}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setIntervening(false)}>
              Cancel
            </Button>
            <Button disabled={sendingNote || !noteText.trim()} onClick={() => void sendNote()}>
              Send
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <div className="mt-10 space-y-10">
        <Panel>
          <div className="space-y-3">
            <MetaLine>
              <span className="capitalize">{mission.kind}</span>
              {mission.route && <span>route {mission.route}</span>}
              {mission.plan_route && <span>plan route {mission.plan_route}</span>}
              {executorActivity && (
                <span>
                  harness {executorActivity.turns} turn{executorActivity.turns === 1 ? '' : 's'},{' '}
                  {executorActivity.tool_calls} tool call{executorActivity.tool_calls === 1 ? '' : 's'}
                  {executorActivity.worktree && (
                    <>
                      {' · '}
                      {executorActivity.worktree.untracked} new · {executorActivity.worktree.modified} modified
                      {executorActivity.worktree.newest_mtime > 0 &&
                        ` · changed ${relativeTime(new Date(executorActivity.worktree.newest_mtime * 1000).toISOString())}`}
                    </>
                  )}
                </span>
              )}
              {!isTerminal && mission.iteration > 0 && <span>retries {mission.iteration}</span>}
              {mission.budget_amount != null && (
                <span>budget {money(mission.budget_amount, mission.budget_currency ?? 'USD')}</span>
              )}
              {mission.branch && (
                <span className="font-mono">
                  {mission.branch} @ {mission.base_commit?.slice(0, 8)}
                </span>
              )}
              {mission.repo_url && (
                <a
                  href={githubHTMLURL(mission.repo_url)}
                  target="_blank"
                  rel="noreferrer"
                  className="underline underline-offset-2 hover:text-foreground"
                >
                  {githubFullName(mission.repo_url)}
                </a>
              )}
              {prChip && (
                <a href={prChip.url} target="_blank" rel="noreferrer" className="underline underline-offset-2 hover:text-foreground">
                  PR #{prChip.number}
                </a>
              )}
              <span title={relativeTime(mission.created_at)}>created {euDateTime(mission.created_at)}</span>
            </MetaLine>
            <div className="flex flex-wrap items-center gap-1.5">
              {costEntries.map(([currency, cost]) => (
                <Badge key={currency} variant="secondary" className="tabular-nums">
                  {money(cost, currency)}
                </Badge>
              ))}
                {usage &&
                  usage.requests > 0 &&
                  usage.models
                    .filter((m) => !m.harness)
                    .map((m) => (
                      <ModelBadge
                        key={`${m.provider}:${m.model}`}
                        provider={m.provider}
                        model={m.model}
                        title={`${m.requests} call${m.requests === 1 ? '' : 's'} via ${m.provider}`}
                      />
                    ))}
                {executorSpawn && (
                  <Badge
                    variant="secondary"
                    aria-label={`${harnessLabel(executorSpawn.harness)} harness`}
                    title={`Delegated CLI harness (${harnessLabel(executorSpawn.harness)}) that ran this mission's coding work, via ${executorSpawn.provider}`}
                  >
                    <HarnessIcon harness={executorSpawn.harness} />
                    {executorSpawn.model}
                  </Badge>
                )}
                {mission.environment &&
                  (() => {
                    const EnvIcon = envIcon(mission.environment)
                    const label = `${mission.environment} environment`
                    return (
                      <Badge
                        variant="secondary"
                        aria-label={EnvIcon ? label : undefined}
                        title={EnvIcon ? label : "Sandbox environment this mission's container runs"}
                      >
                        {EnvIcon ? <EnvIcon /> : `env · ${mission.environment}`}
                      </Badge>
                    )
                  })()}
                {githubModes.includes('push') && (
                  <Badge variant="secondary" title="This mission pushes its branch automatically when it finishes">
                    auto-push
                  </Badge>
                )}
                {githubModes.includes('push_pr') && (
                  <Badge
                    variant="secondary"
                    title="This mission pushes its branch and opens a pull request automatically when it finishes"
                  >
                    auto-PR
                  </Badge>
                )}
                {usage && usage.requests > 0 && (
                  <Badge variant="secondary" title="input→output tokens; cached = input read from the provider's prompt cache">
                    {compact(usage.input_tokens)}→{compact(usage.output_tokens)} tok
                    {usage.cache_read_tokens ? ` · ${compact(usage.cache_read_tokens)} cached` : ''}
                  </Badge>
                )}
                {usage && usage.review_input_tokens ? (
                  <Badge
                    variant="secondary"
                    title={
                      usage.review_token_ceiling
                        ? 'Input tokens spent on review turns against the per-mission review token ceiling'
                        : 'Input tokens spent on review turns (no ceiling set)'
                    }
                  >
                    review {compact(usage.review_input_tokens)}
                    {usage.review_token_ceiling ? ` / ${compact(usage.review_token_ceiling)}` : ''} tok
                  </Badge>
                ) : null}
                <Badge variant="secondary" title="Time spent actively processing">
                  proc {formatDuration(processingMs)}
                </Badge>
                <Badge variant="secondary" title="Wall-clock time since the mission started">
                  total {formatDuration(elapsedMs)}
                </Badge>
                <Badge variant="secondary">
                  {turns} turn{turns === 1 ? '' : 's'}
                </Badge>
                {usage && usage.requests > 0 && (
                  <>
                    <Badge variant="secondary">
                      {usage.requests} call{usage.requests === 1 ? '' : 's'}
                    </Badge>
                    {usage.unpriced_requests > 0 && (
                      <Badge variant="secondary" title="Some calls have no configured price; their cost is not included.">
                        {usage.unpriced_requests} unpriced call{usage.unpriced_requests === 1 ? '' : 's'}
                      </Badge>
                    )}
                    {mission.budget_amount != null &&
                      mission.budget_amount > 0 &&
                      (() => {
                        const pct = budgetPercentSpent(usage, mission.budget_currency ?? 'USD', mission.budget_amount)
                        return pct == null ? null : <Badge variant="secondary">{pct}% of budget</Badge>
                      })()}
                  </>
                )}
            </div>
            {costEntries.map(([currency]) => {
              const line = usage && unbilledTooltipLine(usage, currency)
              return line ? (
                <p key={currency} className="text-xs text-muted-foreground">
                  {line}
                </p>
              ) : null
            })}
            {costEntries
              .filter(([currency]) => mission.budget_amount != null && mission.budget_currency === currency)
              .map(([currency, cost]) => (
                <CostDisplay key={currency} cost={cost} currency={currency} budget={mission.budget_amount ?? undefined} />
              ))}
          </div>
        </Panel>

        <GoalSection goal={mission.goal} />

        {mission.discover_notes && <DiscoverSection notes={mission.discover_notes} />}

        {(mission.plan?.units?.length ?? 0) > 0 && (
          <Panel title="Plan">
            <PlanSection units={mission.plan?.units ?? []} assumptions={mission.plan?.assumptions} />
          </Panel>
        )}

        {(mission.review_findings?.length ?? 0) > 0 && (
          <Panel title="Review findings">
            <FindingsSection findings={mission.review_findings ?? []} />
          </Panel>
        )}

        {isTerminal && (runsPlanless(mission) ? mission.final_output : mission.last_evidence) && (
          <Panel
            title="Result"
            actions={<CopyButton value={(runsPlanless(mission) ? mission.final_output : mission.last_evidence) ?? ''} label="Copy result" />}
          >
            <ResultSection evidence={(runsPlanless(mission) ? mission.final_output : mission.last_evidence) ?? ''} />
          </Panel>
        )}

        {mission.destinations && mission.destinations.length > 0 && (
          <Panel title="Destinations">
            <div className="divide-y divide-border">
              {mission.destinations.map((d, i) => {
                const row = d.destination_id ? destinationsByID.get(d.destination_id) : undefined
                const isGitHub = row?.kind === 'github'
                const mode = isGitHub ? (row.config as unknown as GitHubDestinationConfig).mode : undefined
                return (
                  <div key={d.destination_id || `${d.destination}-${i}`} className="flex items-center gap-2 py-2 text-sm">
                    <span className="text-xs text-muted-foreground uppercase">
                      {row?.kind ?? d.destination ?? 'destination'}
                    </span>
                    {isGitHub ? (
                      <span className="truncate">
                        <span className="font-medium text-foreground">{row.name}</span>
                        {d.repo_url && (
                          <>
                            {' · '}
                            <a
                              href={githubHTMLURL(d.repo_url)}
                              target="_blank"
                              rel="noreferrer"
                              className="underline underline-offset-2 hover:text-foreground"
                            >
                              {githubFullName(d.repo_url)}
                            </a>
                          </>
                        )}
                        {mode && ` · ${mode === 'push_pr' ? 'push + PR' : 'push'}`}
                        {d.branch && ` · ${d.branch}`}
                        {d.pr_url && (
                          <>
                            {' · '}
                            <a
                              href={d.pr_url}
                              target="_blank"
                              rel="noreferrer"
                              className="underline underline-offset-2 hover:text-foreground"
                            >
                              PR #{d.pr_number}
                            </a>
                          </>
                        )}
                      </span>
                    ) : d.destination === 'kb' ? (
                      <span className="text-muted-foreground">promoted to knowledge base</span>
                    ) : (
                      <span className="text-muted-foreground">{row?.name ?? d.destination_id}</span>
                    )}
                    {d.delivered_at && (
                      <StatusBadge status="success" label="delivered" size="sm" />
                    )}
                    {d.error && <StatusBadge status="error" label="failed" size="sm" />}
                  </div>
                )
              })}
            </div>
          </Panel>
        )}

        <ArtifactsSection
          missionId={id}
          missionName={mission.name}
          phase={mission.phase}
          workspace={mission.workspace}
          refs={mission.artifact_refs ?? []}
        />

        <TimelineSection events={events} />

      </div>
    </PageShell>
  )
}
