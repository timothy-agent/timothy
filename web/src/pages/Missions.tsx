import { BellIcon, CancelCircleIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { listMissions, listNotifications, markNotificationRead } from '../api/client'
import type { Mission, Notification } from '../api/types'
import { MissionCard } from '../components/missions/MissionCard'
import { RecurringSchedules } from '../components/missions/RecurringSchedules'
import { Button } from '../components/ui/button'
import { subscribeEvents } from '../lib/events'

export function Missions() {
  const navigate = useNavigate()
  const [missions, setMissions] = useState<Mission[]>([])
  const [notifications, setNotifications] = useState<Notification[]>([])

  const refresh = useCallback(() => {
    listMissions().then(setMissions, () => undefined)
    listNotifications().then(setNotifications, () => undefined)
  }, [])

  useEffect(() => {
    refresh()
    // Any mission or notification signal means this board is stale —
    // refetch both lists rather than trying to patch just the one
    // that changed. onReady covers the initial connect and every
    // reconnect, catching anything missed while disconnected.
    return subscribeEvents(
      () => refresh(),
      () => refresh(),
    )
  }, [refresh])

  const unread = notifications.filter((n) => !n.read)

  const dismiss = (id: string) => {
    markNotificationRead(id).then(refresh, () => undefined)
  }

  return (
    <div className="mx-auto w-full max-w-full px-8 py-6">
      <div className="flex items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">Missions</h1>
          <p className="text-sm text-muted-foreground">
            Long-running tasks that plan, execute, and review their own work.
          </p>
        </div>
        <Button onClick={() => navigate('/missions/new')}>New mission</Button>
      </div>

      {unread.length > 0 && (
        <div className="mt-4 space-y-2">
          {unread.map((n) => (
            <div
              key={n.id}
              className="flex items-center justify-between gap-3 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200"
            >
              <button
                type="button"
                onClick={() => navigate(`/missions/${n.mission_id}`)}
                className="flex items-center gap-2 text-left hover:underline"
              >
                <HugeiconsIcon icon={BellIcon} className="size-4 shrink-0" />
                {n.message}
              </button>
              <button
                type="button"
                onClick={() => dismiss(n.id)}
                className="shrink-0 text-amber-700 hover:text-amber-900 dark:text-amber-400 dark:hover:text-amber-200"
                aria-label="Dismiss"
              >
                <HugeiconsIcon icon={CancelCircleIcon} className="size-4" />
              </button>
            </div>
          ))}
        </div>
      )}

      <RecurringSchedules />

      <div className="mt-6 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {missions.map((m) => (
          <MissionCard key={m.id} mission={m} />
        ))}
        {missions.length === 0 && (
          <div className="col-span-full rounded-xl border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
            No missions yet, create one to get started.
          </div>
        )}
      </div>
    </div>
  )
}
