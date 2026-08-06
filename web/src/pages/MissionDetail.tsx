import { ArrowLeft01Icon } from '@hugeicons-pro/core-stroke-rounded'
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
  resumeMission,
} from '../api/client'
import type { Mission, MissionEvent, MissionUsage, Schedule } from '../api/types'
import { ArtifactsSection } from '../components/missions/ArtifactsSection'
import { PermissionBanner } from '../components/missions/PermissionBanner'
import { PlanSection } from '../components/missions/PlanSection'
import { ProgressSection } from '../components/missions/ProgressSection'
import { PushBranchDialog } from '../components/missions/PushBranchDialog'
import { ResultSection } from '../components/missions/ResultSection'
import { TimelineSection } from '../components/missions/TimelineSection'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../components/ui/dialog'
import { formatDuration } from '../components/Activity'
import { errText } from '../components/settings/util'
import { describeCron } from '../lib/schedules'
import { playAlertSound } from '../lib/alertSound'
import { subscribeEvents } from '../lib/events'

function formatDate(v?: string): string {
  if (!v) return 'N/A'
  return new Date(v).toLocaleString()
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

export function MissionDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [mission, setMission] = useState<Mission | null>(null)
  const [events, setEvents] = useState<MissionEvent[]>([])
  const [usage, setUsage] = useState<MissionUsage | null>(null)
  const [schedule, setSchedule] = useState<Schedule | null>(null)
  const [busy, setBusy] = useState(false)
  const [pushOpen, setPushOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
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

  const { turns, processingMs } = turnStats(events)
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
            <h1 className="text-xl font-semibold tracking-tight">{mission.goal}</h1>
            <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-sm text-muted-foreground">
              <span className="capitalize">{mission.kind}</span>
              <span>{mission.phase}</span>
              <span>{mission.status.replace(/_/g, ' ')}</span>
              {!terminalPhases.has(mission.phase) && mission.iteration > 0 && (
                <span>Retries {mission.iteration}</span>
              )}
              {mission.budget_amount != null && (
                <span>
                  budget {mission.budget_currency ?? 'USD'} {mission.budget_amount}
                </span>
              )}
            </div>
            {mission.branch && (
              <p className="mt-1 text-xs text-muted-foreground">
                {mission.branch} @ {mission.base_commit?.slice(0, 8)}
              </p>
            )}
            {schedule && (
              <p className="mt-1 text-xs text-muted-foreground">
                Recurring · {describeCron(schedule.cron)} · next run {formatDate(schedule.next_run)}
              </p>
            )}
            {pauseDetail && (
              <p className="mt-2 text-sm text-amber-700 dark:text-amber-400">{pauseDetail}</p>
            )}
          </div>
          <div className="flex shrink-0 gap-2">
            {mission.kind === 'coding' && mission.branch && (
              <Button variant="outline" onClick={() => setPushOpen(true)}>
                Push branch
              </Button>
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
              <Button variant="destructive" disabled={busy} onClick={() => setConfirmDelete(true)}>
                Delete
              </Button>
            )}
          </div>
        </div>
        <div className="mt-3 flex flex-wrap gap-x-6 gap-y-1 text-sm text-muted-foreground">
          <span>{turns} turn{turns === 1 ? '' : 's'}</span>
          <span>Processing {formatDuration(processingMs)}</span>
          <span>Elapsed {formatDuration(elapsedMs)}</span>
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

      {usage && usage.requests > 0 && (
        <section>
          <h2 className="mb-2 text-sm font-semibold tracking-tight">Spend</h2>
          <div className="flex flex-wrap gap-x-6 gap-y-1 text-sm text-muted-foreground">
            {usage.converted_cost_by_currency && Object.keys(usage.converted_cost_by_currency).length > 0 ? (
              Object.entries(usage.converted_cost_by_currency).map(([currency, cost]) => (
                <span key={currency} className="text-foreground" title="Converted from the billed amount(s) below using a stored exchange rate.">
                  {currency} {cost.toFixed(4)}
                  <span className="ml-1 text-xs font-normal text-muted-foreground">
                    ({Object.entries(usage.cost_by_currency).map(([c, v]) => `${c} ${v.toFixed(4)}`).join(', ')} billed)
                  </span>
                </span>
              ))
            ) : (
              Object.entries(usage.cost_by_currency).map(([currency, cost]) => (
                <span key={currency} className="text-foreground">
                  {currency} {cost.toFixed(4)}
                </span>
              ))
            )}
            <span>{usage.requests} model calls</span>
            <span>
              {usage.input_tokens.toLocaleString()} in / {usage.output_tokens.toLocaleString()} out
            </span>
            {mission.budget_amount != null && mission.budget_amount > 0 && (
              <span>
                {Math.round(
                  ((usage.cost_by_currency[mission.budget_currency ?? 'USD'] ?? 0) /
                    mission.budget_amount) *
                    100,
                )}
                % of budget
              </span>
            )}
            {usage.unpriced_requests > 0 && (
              <span title="Some calls have no configured price; their cost is not included.">
                {usage.unpriced_requests} unpriced call{usage.unpriced_requests === 1 ? '' : 's'}
              </span>
            )}
          </div>
          {usage.models.length > 0 && (
            <div className="mt-2 flex flex-wrap gap-1.5">
              {usage.models.map((m) => (
                <Badge
                  key={`${m.provider}:${m.model}`}
                  variant="secondary"
                  title={`${m.requests} call${m.requests === 1 ? '' : 's'} via ${m.provider}`}
                >
                  {m.model}
                </Badge>
              ))}
            </div>
          )}
        </section>
      )}

      <section>
        <h2 className="mb-2 text-sm font-semibold tracking-tight">Plan</h2>
        <PlanSection units={mission.spec?.units ?? []} />
      </section>

      {mission.workspace && (
        <section>
          <h2 className="mb-2 text-sm font-semibold tracking-tight">Artifacts</h2>
          <ArtifactsSection missionId={id} phase={mission.phase} workspace={mission.workspace} />
        </section>
      )}

      <section>
        <h2 className="mb-2 text-sm font-semibold tracking-tight">Progress</h2>
        <ProgressSection notes={mission.progress} />
      </section>

      <section>
        <h2 className="mb-2 text-sm font-semibold tracking-tight">Timeline</h2>
        <TimelineSection events={events} />
      </section>

      {terminalPhases.has(mission.phase) && mission.last_evidence && (
        <section>
          <h2 className="mb-2 text-sm font-semibold tracking-tight">Result</h2>
          <ResultSection evidence={mission.last_evidence} />
        </section>
      )}

      <PushBranchDialog
        missionId={id}
        open={pushOpen}
        onOpenChange={setPushOpen}
        onPushed={refresh}
      />
    </div>
  )
}
