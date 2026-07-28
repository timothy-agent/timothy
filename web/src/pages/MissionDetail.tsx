import { ArrowLeft01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router'
import { toast } from 'sonner'
import {
  answerMissionPermission,
  cancelMission,
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
import { TimelineSection } from '../components/missions/TimelineSection'
import { Badge } from '../components/ui/badge'
import { Button } from '../components/ui/button'
import { errText } from '../components/settings/util'
import { describeCron } from '../lib/schedules'
import { playAlertSound } from '../lib/alertSound'
import { subscribeEvents } from '../lib/events'

function formatDate(v?: string): string {
  if (!v) return '—'
  return new Date(v).toLocaleString()
}

const resumableStatuses = new Set(['paused', 'waiting_for_input'])
const terminalPhases = new Set(['done', 'failed'])

export function MissionDetail() {
  const { id } = useParams<{ id: string }>()
  const [mission, setMission] = useState<Mission | null>(null)
  const [events, setEvents] = useState<MissionEvent[]>([])
  const [usage, setUsage] = useState<MissionUsage | null>(null)
  const [schedule, setSchedule] = useState<Schedule | null>(null)
  const [busy, setBusy] = useState(false)
  const [pushOpen, setPushOpen] = useState(false)

  const refresh = useCallback(() => {
    if (!id) return
    getMission(id).then(setMission, () => undefined)
    missionEvents(id).then(setEvents, () => undefined)
    missionUsage(id).then(setUsage, () => undefined)
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
      <div className="mx-auto w-full max-w-5xl px-4 py-6">
        <p className="text-sm text-muted-foreground">Loading…</p>
      </div>
    )
  }

  const canResume = resumableStatuses.has(mission.status)
  const canCancel = !terminalPhases.has(mission.phase)

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

  const decidePermission = async (decision: 'once' | 'session' | 'deny') => {
    try {
      await answerMissionPermission(id, decision)
      refresh()
    } catch (err) {
      toast.error('Could not answer permission request', { description: errText(err) })
    }
  }

  return (
    <div className="mx-auto w-full max-w-5xl space-y-6 px-4 py-6">
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
              <span>
                iteration {mission.iteration}/{mission.max_iterations}
              </span>
              {mission.budget_usd != null && <span>budget ${mission.budget_usd}</span>}
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
          </div>
        </div>
      </div>

      {usage && usage.requests > 0 && (
        <section>
          <h2 className="mb-2 text-sm font-semibold tracking-tight">Spend</h2>
          <div className="flex flex-wrap gap-x-6 gap-y-1 text-sm text-muted-foreground">
            <span className="text-foreground">${usage.cost_usd.toFixed(4)}</span>
            <span>{usage.requests} model calls</span>
            <span>
              {usage.input_tokens.toLocaleString()} in / {usage.output_tokens.toLocaleString()} out
            </span>
            {mission.budget_usd != null && mission.budget_usd > 0 && (
              <span>{Math.round((usage.cost_usd / mission.budget_usd) * 100)}% of budget</span>
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

      <section>
        <h2 className="mb-2 text-sm font-semibold tracking-tight">Progress</h2>
        <ProgressSection notes={mission.progress} />
      </section>

      {mission.workspace && (
        <section>
          <h2 className="mb-2 text-sm font-semibold tracking-tight">Artifacts</h2>
          <ArtifactsSection missionId={id} phase={mission.phase} workspace={mission.workspace} />
        </section>
      )}

      <section>
        <h2 className="mb-2 text-sm font-semibold tracking-tight">Timeline</h2>
        <TimelineSection events={events} />
      </section>

      {terminalPhases.has(mission.phase) && mission.last_evidence && (
        <section>
          <h2 className="mb-2 text-sm font-semibold tracking-tight">Result</h2>
          <div className="rounded-lg border border-border bg-muted/50 p-3">
            <p className="text-sm whitespace-pre-wrap">{mission.last_evidence}</p>
            <p className="mt-2 text-xs text-muted-foreground">
              Worker-reported — not independently verified.
            </p>
          </div>
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
