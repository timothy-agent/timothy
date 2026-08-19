import { BellIcon, CancelCircleIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router'
import { listMissions, listNotifications, markNotificationRead } from '../api/client'
import type { Mission, Notification } from '../api/types'
import { MissionCard } from '../components/missions/MissionCard'
import { Button } from '../components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '../components/ui/select'
import { subscribeEvents } from '../lib/events'

type KindFilter = 'all' | Mission['kind']
type SourceFilter = 'all' | 'manual' | 'automated'

// harnessLabel mirrors MissionCard's own "Native" fallback for an
// unset harness — filter options must read the same as the cards
// they're filtering.
function harnessLabel(harness?: string): string {
  return harness || 'Native'
}

// notificationSeverityClasses colors the banner by notification kind —
// same green/amber/red convention MissionCard's statusColor uses for
// mission status chips. Any kind not listed here (unanticipated future
// kind) falls back to the existing amber styling.
const notificationSeverityClasses: Record<string, string> = {
  done: 'border-green-200 bg-green-50 text-green-900 dark:border-green-900 dark:bg-green-950 dark:text-green-200',
  error:
    'border-red-200 bg-red-50 text-red-900 dark:border-red-900 dark:bg-red-950 dark:text-red-200',
  paused:
    'border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200',
  waiting_for_input:
    'border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200',
}

const notificationDismissClasses: Record<string, string> = {
  done: 'text-green-700 hover:text-green-900 dark:text-green-400 dark:hover:text-green-200',
  error: 'text-red-700 hover:text-red-900 dark:text-red-400 dark:hover:text-red-200',
  paused: 'text-amber-700 hover:text-amber-900 dark:text-amber-400 dark:hover:text-amber-200',
  waiting_for_input:
    'text-amber-700 hover:text-amber-900 dark:text-amber-400 dark:hover:text-amber-200',
}

const defaultSeverityClasses =
  'border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200'
const defaultDismissClasses =
  'text-amber-700 hover:text-amber-900 dark:text-amber-400 dark:hover:text-amber-200'

export function Missions() {
  const navigate = useNavigate()
  const [missions, setMissions] = useState<Mission[]>([])
  const [notifications, setNotifications] = useState<Notification[]>([])
  const [kindFilter, setKindFilter] = useState<KindFilter>('all')
  const [harnessFilter, setHarnessFilter] = useState('all')
  const [modelFilter, setModelFilter] = useState('all')
  const [sourceFilter, setSourceFilter] = useState<SourceFilter>('all')

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

  // Distinct harness/model values present in the loaded missions only —
  // a picker never offers a value nothing on the board actually has.
  const harnessOptions = useMemo(
    () => Array.from(new Set(missions.map((m) => harnessLabel(m.harness)))).sort(),
    [missions],
  )
  const modelOptions = useMemo(
    () =>
      Array.from(new Set(missions.map((m) => m.top_model).filter((v): v is string => !!v))).sort(),
    [missions],
  )

  const filtersActive =
    kindFilter !== 'all' || harnessFilter !== 'all' || modelFilter !== 'all' || sourceFilter !== 'all'

  const filteredMissions = useMemo(
    () =>
      missions.filter((m) => {
        if (kindFilter !== 'all' && m.kind !== kindFilter) return false
        if (harnessFilter !== 'all' && harnessLabel(m.harness) !== harnessFilter) return false
        if (modelFilter !== 'all' && m.top_model !== modelFilter) return false
        if (sourceFilter === 'manual' && m.schedule_id) return false
        if (sourceFilter === 'automated' && !m.schedule_id) return false
        return true
      }),
    [missions, kindFilter, harnessFilter, modelFilter, sourceFilter],
  )

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
              className={`flex items-center justify-between gap-3 rounded-lg border px-3 py-2 text-sm ${notificationSeverityClasses[n.kind] ?? defaultSeverityClasses}`}
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
                className={`shrink-0 ${notificationDismissClasses[n.kind] ?? defaultDismissClasses}`}
                aria-label="Dismiss"
              >
                <HugeiconsIcon icon={CancelCircleIcon} className="size-4" />
              </button>
            </div>
          ))}
        </div>
      )}

      <div className="mt-4 flex flex-wrap items-center gap-2">
        <Select value={kindFilter} onValueChange={(v) => setKindFilter(v as KindFilter)}>
          <SelectTrigger size="sm" aria-label="Filter by kind">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All kinds</SelectItem>
            <SelectItem value="coding">Coding</SelectItem>
            <SelectItem value="general">General</SelectItem>
          </SelectContent>
        </Select>

        <Select value={harnessFilter} onValueChange={setHarnessFilter}>
          <SelectTrigger size="sm" aria-label="Filter by harness">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All harnesses</SelectItem>
            {harnessOptions.map((h) => (
              <SelectItem key={h} value={h}>
                {h}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select value={modelFilter} onValueChange={setModelFilter}>
          <SelectTrigger size="sm" aria-label="Filter by model">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All models</SelectItem>
            {modelOptions.map((m) => (
              <SelectItem key={m} value={m}>
                {m}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select value={sourceFilter} onValueChange={(v) => setSourceFilter(v as SourceFilter)}>
          <SelectTrigger size="sm" aria-label="Filter by source">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All sources</SelectItem>
            <SelectItem value="manual">Manual</SelectItem>
            <SelectItem value="automated">Automated</SelectItem>
          </SelectContent>
        </Select>

        {filtersActive && (
          <span className="text-xs text-muted-foreground">
            {filteredMissions.length} of {missions.length}
          </span>
        )}
      </div>

      <div className="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {filteredMissions.map((m) => (
          <MissionCard key={m.id} mission={m} />
        ))}
        {filteredMissions.length === 0 && (
          <div className="col-span-full rounded-xl border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
            {missions.length === 0
              ? 'No missions yet, create one to get started.'
              : 'No missions match the current filters.'}
          </div>
        )}
      </div>
    </div>
  )
}
