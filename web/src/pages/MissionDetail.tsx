import { ArrowLeft01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { Link, useParams } from 'react-router'
import { toast } from 'sonner'
import {
  answerMissionPermission,
  cancelMission,
  getMission,
  missionEvents,
  resumeMission,
} from '../api/client'
import type { Mission, MissionEvent } from '../api/types'
import { PermissionBanner } from '../components/missions/PermissionBanner'
import { PlanSection } from '../components/missions/PlanSection'
import { ProgressSection } from '../components/missions/ProgressSection'
import { TimelineSection } from '../components/missions/TimelineSection'
import { Button } from '../components/ui/button'
import { errText } from '../components/settings/util'

const pollIntervalIdle = 5000
const pollIntervalPending = 1500

const resumableStatuses = new Set(['paused', 'waiting_for_input'])
const terminalPhases = new Set(['done', 'failed'])

export function MissionDetail() {
  const { id } = useParams<{ id: string }>()
  const [mission, setMission] = useState<Mission | null>(null)
  const [events, setEvents] = useState<MissionEvent[]>([])
  const [busy, setBusy] = useState(false)

  const refresh = useCallback(() => {
    if (!id) return
    getMission(id).then(setMission, () => undefined)
    missionEvents(id).then(setEvents, () => undefined)
  }, [id])

  useEffect(() => {
    refresh()
    const interval = mission?.pending_permission ? pollIntervalPending : pollIntervalIdle
    const timer = setInterval(refresh, interval)
    return () => clearInterval(timer)
  }, [refresh, mission?.pending_permission])

  if (!id) return null
  if (!mission) {
    return (
      <div className="mx-auto w-full max-w-3xl px-4 py-6">
        <p className="text-sm text-muted-foreground">Loading…</p>
      </div>
    )
  }

  const canResume = resumableStatuses.has(mission.status)
  const canCancel = !terminalPhases.has(mission.phase)

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
    <div className="mx-auto w-full max-w-3xl space-y-6 px-4 py-6">
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
            {mission.pause_message && (
              <p className="mt-2 text-sm text-amber-700 dark:text-amber-400">{mission.pause_message}</p>
            )}
          </div>
          <div className="flex shrink-0 gap-2">
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

      <section>
        <h2 className="mb-2 text-sm font-semibold tracking-tight">Plan</h2>
        <PlanSection units={mission.spec?.units ?? []} />
      </section>

      <section>
        <h2 className="mb-2 text-sm font-semibold tracking-tight">Progress</h2>
        <ProgressSection notes={mission.progress} />
      </section>

      <section>
        <h2 className="mb-2 text-sm font-semibold tracking-tight">Timeline</h2>
        <TimelineSection events={events} />
      </section>
    </div>
  )
}
