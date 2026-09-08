import { Bell, Inbox, X } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router'
import { listMissions, listNotifications, markNotificationRead } from '../api/client'
import type { Mission, Notification } from '../api/types'
import { MissionCard } from '../components/missions/MissionCard'
import { EmptyState } from '../components/timothy/empty-state'
import { IconButton } from '../components/timothy/icon-button'
import { PageHeader } from '../components/timothy/page-header'
import { PageShell } from '../components/timothy/page-shell'
import { Alert } from '../components/ui/alert'
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

// notificationTone maps a notification kind to the Alert tone that
// reads the same as the mission status it reports (section 13).
function notificationTone(kind: string): 'good' | 'destructive' | 'warning' {
  if (kind === 'done' || kind === 'ask_timed_out') return 'good'
  if (kind === 'error') return 'destructive'
  return 'warning'
}

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
    <PageShell>
      <PageHeader
        title="Missions"
        description="Long-running tasks that plan, execute, and review their own work."
        actions={<Button onClick={() => navigate('/missions/new')}>New mission</Button>}
      />

      {unread.length > 0 && (
        <div className="mb-6 space-y-2">
          {unread.map((n) => (
            <Alert key={n.id} tone={notificationTone(n.kind)} className="flex items-center justify-between gap-3">
              <button
                type="button"
                onClick={() => navigate(`/missions/${n.mission_id}`)}
                className="flex items-center gap-2 text-left hover:underline"
              >
                <Bell aria-hidden className="size-4 shrink-0" />
                {n.message}
              </button>
              <IconButton
                label="Dismiss"
                icon={X}
                variant="ghost"
                size="xs"
                onClick={() => dismiss(n.id)}
              />
            </Alert>
          ))}
        </div>
      )}

      <div className="mb-4 flex flex-wrap items-center gap-2">
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

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {filteredMissions.map((m) => (
          <MissionCard key={m.id} mission={m} />
        ))}
        {filteredMissions.length === 0 && (
          <div className="col-span-full rounded-md border border-dashed border-border">
            <EmptyState
              icon={Inbox}
              title={
                missions.length === 0
                  ? 'No missions yet, create one to get started.'
                  : 'No missions match the current filters.'
              }
              action={
                missions.length === 0 ? (
                  <Button onClick={() => navigate('/missions/new')}>New mission</Button>
                ) : undefined
              }
            />
          </div>
        )}
      </div>
    </PageShell>
  )
}
