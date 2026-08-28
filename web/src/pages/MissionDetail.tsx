import {
  ArrowLeft01Icon,
  CloudUploadIcon,
  Delete02Icon,
  GitBranchIcon,
  GitPullRequestCreateIcon,
  Pdf02Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { toast } from 'sonner'
import {
  answerMissionPermission,
  cancelMission,
  deleteMission,
  getMission,
  listSchedules,
  missionEvents,
  missionUsage,
  openMissionPR,
  pushMission,
  resumeMission,
  sendMissionNote,
} from '../api/client'
import type { Mission, MissionEvent, MissionPROpenedPayload, MissionUsage, Schedule } from '../api/types'
import { ArtifactRefsSection } from '../components/missions/ArtifactRefsSection'
import { ArtifactsSection } from '../components/missions/ArtifactsSection'
import { PermissionBanner } from '../components/missions/PermissionBanner'
import { PlanSection } from '../components/missions/PlanSection'
import { ExploreSection } from '../components/missions/ExploreSection'
import { GoalSection } from '../components/missions/GoalSection'
import { ResultSection } from '../components/missions/ResultSection'
import { TimelineSection } from '../components/missions/TimelineSection'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../components/ui/dialog'
import { ModelBadge } from '../components/ModelBadge'
import { ClaudeCodeIcon } from '../components/icons/ClaudeCodeIcon'
import { CursorIcon } from '../components/icons/CursorIcon'
import { OpenAIIcon } from '../components/icons/OpenAIIcon'
import { OpenCodeIcon } from '../components/icons/OpenCodeIcon'
import { PiIcon } from '../components/icons/PiIcon'
import { envIcon } from '../components/icons/EnvIcons'
import { Badge } from '../components/ui/badge'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '../components/ui/tooltip'
import { errText } from '../components/settings/util'
import { describeCron } from '../lib/schedules'
import { playAlertSound } from '../lib/alertSound'
import { subscribeEvents } from '../lib/events'
import { compact, formatDuration, missionDisplayName, money, relativeTime } from '../lib/format'

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
function latestExecutorProgress(events: MissionEvent[]): { turns: number; tool_calls: number } | null {
  for (let i = events.length - 1; i >= 0; i--) {
    if (events[i].kind === 'executor.progress') {
      const { turns, tool_calls } = events[i].payload as { turns: number; tool_calls: number }
      return { turns, tool_calls }
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

// harnessDisplayName maps a registered harness id to the label shown
// in the pill — mirrors MissionForm's executorChoices labels.
const harnessDisplayNames: Record<string, string> = {
  'claude-cli': 'Claude Code',
  pi: 'pi',
  'codex-cli': 'Codex CLI',
  opencode: 'OpenCode',
  'cursor-cli': 'Cursor CLI',
}

function harnessDisplayName(harness: string): string {
  return harnessDisplayNames[harness] ?? harness
}

// HarnessIcon picks the pill's mark for a harness id, defaulting to
// the Claude Code mark for ids without one of their own.
function HarnessIcon({ harness }: { harness: string }) {
  if (harness === 'pi') return <PiIcon />
  if (harness === 'codex-cli') return <OpenAIIcon />
  if (harness === 'opencode') return <OpenCodeIcon />
  if (harness === 'cursor-cli') return <CursorIcon />
  return <ClaudeCodeIcon />
}

// CostBadge renders the billed-cost pill: a plain Badge when there's
// no unbilled (subscription) cost to note, or one wrapped in a Tooltip
// showing that single line when there is.
function CostBadge({ cost, currency, unbilledLine }: { cost: number; currency: string; unbilledLine: string | null }) {
  const badge = <Badge variant="secondary">{money(cost, currency)}</Badge>
  if (!unbilledLine) return badge
  return (
    <Tooltip>
      <TooltipTrigger asChild>{badge}</TooltipTrigger>
      <TooltipContent>{unbilledLine}</TooltipContent>
    </Tooltip>
  )
}

export function MissionDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [mission, setMission] = useState<Mission | null>(null)
  const [events, setEvents] = useState<MissionEvent[]>([])
  const [usage, setUsage] = useState<MissionUsage | null>(null)
  const [schedule, setSchedule] = useState<Schedule | null>(null)
  const [busy, setBusy] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
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

  if (!id) return null
  if (!mission) {
    return (
      <div className="mx-auto w-full max-w-full px-8 py-6">
        <p className="text-sm text-muted-foreground">Loading…</p>
      </div>
    )
  }

  const canResume = resumableStatuses.has(mission.status)
  const canCancel = !terminalPhases.has(mission.phase)
  // isGitHubConnection: a coding mission cloned through a connector —
  // gets the two-button push affordance; every other mission keeps the
  // existing single push flow (currently: none rendered — see slice 3
  // notes). prChip prefers the optimistic click response over the
  // timeline so it appears the instant the PR is opened, falling back
  // to the durable mission.pr_opened event on reload/cross-tab.
  const isGitHubConnection = !!mission.connector_id
  const prChip = prInfo ?? latestPROpened(events)

  const { turns, processingMs } = turnStats(events)
  const executorActivity = terminalPhases.has(mission.phase) ? null : latestExecutorProgress(events)
  const executorSpawn = latestExecutorSpawn(events)
  // A live mission's elapsed span runs to now, not its last updated_at
  // (which only moves on a state transition, not while a turn is
  // in-flight) — otherwise "Elapsed" would understate a mission stuck
  // mid-turn.
  const elapsedEnd = terminalPhases.has(mission.phase) ? mission.updated_at : new Date().toISOString()
  const elapsedMs = new Date(elapsedEnd).getTime() - new Date(mission.created_at).getTime()

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
    <div className="mx-auto w-full max-w-full space-y-6 px-8 py-6">
      <Link
        to="/missions"
        className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground transition hover:text-foreground"
      >
        <HugeiconsIcon icon={ArrowLeft01Icon} className="size-4" />
        Missions
      </Link>

      {mission.pending_permission && (
        <PermissionBanner
          tool={mission.pending_permission_tool}
          args={mission.pending_permission_args}
          danger={mission.pending_permission_danger}
          rationale={mission.pending_permission_rationale}
          answeredDecision={answeredDecision}
          onDecide={(d) => void decidePermission(d)}
        />
      )}

      <div className="border-b border-border pb-6">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h1 className="text-xl font-semibold tracking-tight">{missionDisplayName(mission)}</h1>
            <div className="mt-1.5">
              <GoalSection goal={mission.goal} />
            </div>
            <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-sm text-muted-foreground">
              <span className="capitalize">{mission.kind}</span>
              <span>{mission.phase}</span>
              <span>{mission.status.replace(/_/g, ' ')}</span>
              <span title={new Date(mission.created_at).toLocaleString()}>
                created {relativeTime(mission.created_at)}
              </span>
              {executorActivity && (
                <span>
                  harness: {executorActivity.turns} turn{executorActivity.turns === 1 ? '' : 's'},{' '}
                  {executorActivity.tool_calls} tool call{executorActivity.tool_calls === 1 ? '' : 's'}
                </span>
              )}
              {!terminalPhases.has(mission.phase) && mission.iteration > 0 && (
                <span>Retries {mission.iteration}</span>
              )}
              {mission.budget_amount != null && (
                <span>budget {money(mission.budget_amount, mission.budget_currency ?? 'USD')}</span>
              )}
              {mission.route && <span>route: {mission.route}</span>}
              {mission.plan_route && <span>plan route: {mission.plan_route}</span>}
            </div>
            {mission.branch && (
              <p className="mt-1 text-xs text-muted-foreground">
                {mission.branch} @ {mission.base_commit?.slice(0, 8)}
                {mission.repo_url && (
                  <>
                    {' · '}
                    <a
                      href={githubHTMLURL(mission.repo_url)}
                      target="_blank"
                      rel="noreferrer"
                      className="underline underline-offset-2 hover:text-foreground"
                    >
                      {githubFullName(mission.repo_url)}
                    </a>
                  </>
                )}
                {prChip && (
                  <>
                    {' · '}
                    <a
                      href={prChip.url}
                      target="_blank"
                      rel="noreferrer"
                      className="underline underline-offset-2 hover:text-foreground"
                    >
                      PR #{prChip.number}
                    </a>
                  </>
                )}
              </p>
            )}
            {schedule && (
              <p className="mt-1 text-xs text-muted-foreground">
                Recurring · {describeCron(schedule.cron)} · next run {formatDate(schedule.next_run)}
              </p>
            )}
            {mission.parent_mission_id && (
              <p className="mt-1 text-xs text-muted-foreground">
                Follow-up of{' '}
                <Link
                  to={`/missions/${mission.parent_mission_id}`}
                  className="underline underline-offset-2 hover:text-foreground"
                >
                  {mission.parent_mission_id.slice(0, 8)}
                </Link>
              </p>
            )}
            {mission.attachments && mission.attachments.length > 0 && (
              // Plain chips, not download links: the download path would
              // need the bearer-token blob flow client.ts's
              // fetchAttachmentBlob/fetchBlobDownload use elsewhere, which
              // felt like more plumbing than this summary line warrants.
              <div className="mt-1 flex flex-wrap gap-1.5">
                {mission.attachments.map((a) => (
                  <span
                    key={a.id}
                    className="inline-flex items-center gap-1 rounded-lg border border-border bg-muted/30 px-2 py-0.5 text-xs text-muted-foreground"
                  >
                    <HugeiconsIcon icon={Pdf02Icon} className="size-3" />
                    {a.name ?? a.id.slice(0, 8)}
                  </span>
                ))}
              </div>
            )}
            {pauseDetail && (
              <p className="mt-2 text-sm text-amber-700 dark:text-amber-400">{pauseDetail}</p>
            )}
            {!terminalPhases.has(mission.phase) && (
              <div className="mt-3 flex max-w-md gap-2">
                <Input
                  placeholder="Send a note to steer this mission…"
                  value={noteText}
                  onChange={(e) => setNoteText(e.target.value)}
                  onKeyDown={(e) => e.key === 'Enter' && void sendNote()}
                  disabled={sendingNote}
                />
                <Button
                  variant="outline"
                  disabled={sendingNote || !noteText.trim()}
                  onClick={() => void sendNote()}
                >
                  Send note
                </Button>
              </div>
            )}
          </div>
          <TooltipProvider>
            <div className="flex shrink-0 gap-2">
              {isGitHubConnection && mission.branch && (
                <>
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <Button
                        variant="outline"
                        size="icon"
                        aria-label="Push branch"
                        disabled={pushing}
                        onClick={() => void push()}
                      >
                        <HugeiconsIcon icon={CloudUploadIcon} />
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent>Push branch to GitHub</TooltipContent>
                  </Tooltip>
                  {!prChip && (
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <Button
                          variant="outline"
                          size="icon"
                          aria-label="Push & open PR"
                          disabled={openingPR}
                          onClick={() => void openPR()}
                        >
                          <HugeiconsIcon icon={GitPullRequestCreateIcon} />
                        </Button>
                      </TooltipTrigger>
                      <TooltipContent>Push and open a pull request</TooltipContent>
                    </Tooltip>
                  )}
                </>
              )}
              {canResume && (
                <Button variant="outline" disabled={busy} onClick={() => void resume()}>
                  Resume
                </Button>
              )}
              {canCancel && (
                <Button variant="destructive" disabled={busy} onClick={() => void cancel()}>
                  Cancel
                </Button>
              )}
              {terminalPhases.has(mission.phase) && (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="outline"
                      size="icon"
                      aria-label="Fork"
                      onClick={() => navigate(`/missions/new?parent=${mission.id}`)}
                    >
                      <HugeiconsIcon icon={GitBranchIcon} />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>Fork this mission</TooltipContent>
                </Tooltip>
              )}
              {terminalPhases.has(mission.phase) && (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="destructive"
                      size="icon"
                      aria-label="Delete mission"
                      disabled={busy}
                      onClick={() => setConfirmDelete(true)}
                    >
                      <HugeiconsIcon icon={Delete02Icon} />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>Delete mission</TooltipContent>
                </Tooltip>
              )}
            </div>
          </TooltipProvider>
        </div>
        <div className="mt-3 flex flex-wrap items-center gap-1.5">
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
              aria-label={`${harnessDisplayName(executorSpawn.harness)} harness`}
              title={`Delegated CLI harness (${harnessDisplayName(executorSpawn.harness)}) that ran this mission's coding work, via ${executorSpawn.provider}`}
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
          {mission.on_complete === 'push' && (
            <Badge variant="secondary" title="This mission pushes its branch automatically when it finishes">
              auto-push
            </Badge>
          )}
          {mission.on_complete === 'push_pr' && (
            <Badge
              variant="secondary"
              title="This mission pushes its branch and opens a pull request automatically when it finishes"
            >
              auto-PR
            </Badge>
          )}
          {usage && usage.requests > 0 && (
            <Badge variant="secondary">
              {compact(usage.input_tokens)}→{compact(usage.output_tokens)} tok
            </Badge>
          )}
          <Badge variant="secondary" title="Time spent actively processing">
            proc {formatDuration(processingMs)}
          </Badge>
          <Badge variant="secondary" title="Wall-clock time since the mission started">
            total {formatDuration(elapsedMs)}
          </Badge>
          {usage && usage.requests > 0 && (
            <TooltipProvider>
              {usage.converted_cost_by_currency && Object.keys(usage.converted_cost_by_currency).length > 0 ? (
                Object.entries(usage.converted_cost_by_currency).map(([currency, cost]) => (
                  <CostBadge
                    key={currency}
                    cost={cost}
                    currency={currency}
                    unbilledLine={unbilledTooltipLine(usage, currency)}
                  />
                ))
              ) : (
                Object.entries(usage.cost_by_currency).map(([currency, cost]) => (
                  <CostBadge
                    key={currency}
                    cost={cost}
                    currency={currency}
                    unbilledLine={unbilledTooltipLine(usage, currency)}
                  />
                ))
              )}
            </TooltipProvider>
          )}
          <Badge variant="secondary">
            {turns} turn{turns === 1 ? '' : 's'}
          </Badge>
          {usage && usage.requests > 0 && (
            <>
              <Badge variant="secondary">
                {usage.requests} call{usage.requests === 1 ? '' : 's'}
              </Badge>
              {usage.unpriced_requests > 0 && (
                <Badge
                  variant="secondary"
                  title="Some calls have no configured price; their cost is not included."
                >
                  {usage.unpriced_requests} unpriced call{usage.unpriced_requests === 1 ? '' : 's'}
                </Badge>
              )}
              {mission.budget_amount != null && mission.budget_amount > 0 && (
                <Badge variant="secondary">
                  {Math.round(
                    ((usage.cost_by_currency[mission.budget_currency ?? 'USD'] ?? 0) /
                      mission.budget_amount) *
                      100,
                  )}
                  % of budget
                </Badge>
              )}
            </>
          )}
        </div>
      </div>

      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete this mission?</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            This removes the mission, its events, and its workspace. This cannot be undone.
          </p>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmDelete(false)}>
              Cancel
            </Button>
            <Button variant="destructive" disabled={busy} onClick={() => void remove()}>
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {mission.explore_notes && (
        <section>
          <h2 className="mb-2 text-sm font-semibold tracking-tight">Explore</h2>
          <ExploreSection notes={mission.explore_notes} />
        </section>
      )}

      {(mission.spec?.units?.length ?? 0) > 0 && (
        <section>
          <h2 className="mb-2 text-sm font-semibold tracking-tight">Plan</h2>
          <PlanSection units={mission.spec?.units ?? []} />
        </section>
      )}

      {terminalPhases.has(mission.phase) &&
        (mission.light ? mission.final_output : mission.last_evidence) && (
          <section>
            <h2 className="mb-2 text-sm font-semibold tracking-tight">Result</h2>
            <ResultSection
              evidence={(mission.light ? mission.final_output : mission.last_evidence) ?? ''}
            />
          </section>
        )}

      <ArtifactRefsSection refs={mission.artifact_refs ?? []} />

      <ArtifactsSection missionId={id} phase={mission.phase} workspace={mission.workspace} />

      <section>
        <h2 className="mb-2 text-sm font-semibold tracking-tight">Timeline</h2>
        <TimelineSection events={events} />
      </section>
    </div>
  )
}
