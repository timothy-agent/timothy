import { Bell, Inbox, X } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
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
import { TourOverlay } from '../onboarding/tour/TourOverlay'
import { useTour } from '../onboarding/tour/useTour'
import { missionsTour } from '../onboarding/tours/missions'

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

// Mirrors the server's default page size: a full page means another may exist.
const missionsPageSize = 50

interface Board {
  missions: Mission[]
  hasMore: boolean
}

// sortsAfter is true when a comes after b in the server's
// created_at DESC, id DESC order.
function sortsAfter(a: Mission, b: Mission): boolean {
  const ta = Date.parse(a.created_at)
  const tb = Date.parse(b.created_at)
  return ta < tb || (ta === tb && a.id < b.id)
}

// mergeFirstPage folds a refetched first page into the loaded board:
// page rows replace their stale copies, loaded rows older than the page
// stay, and rows the page should hold but lacks (deleted, no longer
// matching) drop out.
function mergeFirstPage(prev: Board, page: Mission[]): Board {
  if (page.length < missionsPageSize) return { missions: page, hasMore: false }
  const ids = new Set(page.map((m) => m.id))
  const last = page[page.length - 1]
  const older = prev.missions.filter((m) => !ids.has(m.id) && sortsAfter(m, last))
  return { missions: [...page, ...older], hasMore: older.length > 0 ? prev.hasMore : true }
}

// addSeen unions values into a sorted option list, keeping prev when
// nothing is new.
function addSeen(prev: string[], values: string[]): string[] {
  const next = new Set(prev)
  for (const v of values) next.add(v)
  return next.size === prev.length ? prev : Array.from(next).sort()
}

export function Missions() {
  const navigate = useNavigate()
  const [board, setBoard] = useState<Board>({ missions: [], hasMore: false })
  const [notifications, setNotifications] = useState<Notification[]>([])
  const [kindFilter, setKindFilter] = useState<KindFilter>('all')
  const [harnessFilter, setHarnessFilter] = useState('all')
  const [modelFilter, setModelFilter] = useState('all')
  const [sourceFilter, setSourceFilter] = useState<SourceFilter>('all')
  // Harness/model values seen on any loaded page: a picker never offers
  // a value nothing on the board has had.
  const [harnessOptions, setHarnessOptions] = useState<string[]>([])
  const [modelOptions, setModelOptions] = useState<string[]>([])
  const tour = useTour(missionsTour, { enabled: true })
  // gen bumps on every filter change so responses for old filters drop.
  const gen = useRef(0)
  const loadingMore = useRef(false)
  const sentinelRef = useRef<HTMLDivElement>(null)
  const missions = board.missions

  const query = useMemo(
    () => ({
      kind: kindFilter === 'all' ? undefined : kindFilter,
      harness: harnessFilter === 'all' ? undefined : harnessFilter === 'Native' ? 'native' : harnessFilter,
      model: modelFilter === 'all' ? undefined : modelFilter,
      source: sourceFilter === 'all' ? undefined : sourceFilter,
      limit: missionsPageSize,
    }),
    [kindFilter, harnessFilter, modelFilter, sourceFilter],
  )

  const noteSeen = useCallback((page: Mission[]) => {
    setHarnessOptions((prev) => addSeen(prev, page.map((m) => harnessLabel(m.harness))))
    setModelOptions((prev) =>
      addSeen(prev, page.map((m) => m.top_model).filter((v): v is string => !!v)),
    )
  }, [])

  const loadNotifications = useCallback(() => {
    listNotifications({ unread: true }).then(setNotifications, () => undefined)
  }, [])

  // Mount and filter change: page 1 replaces whatever was loaded.
  useEffect(() => {
    const g = ++gen.current
    loadingMore.current = false
    listMissions(query).then((page) => {
      if (g !== gen.current) return
      noteSeen(page)
      setBoard({ missions: page, hasMore: page.length === missionsPageSize })
    }, () => undefined)
  }, [query, noteSeen])

  // A signal refetches page 1 only and merges it; older loaded pages
  // are kept as they are.
  const refresh = useCallback(() => {
    const g = gen.current
    listMissions(query).then((page) => {
      if (g !== gen.current) return
      noteSeen(page)
      setBoard((prev) => mergeFirstPage(prev, page))
    }, () => undefined)
    loadNotifications()
  }, [query, noteSeen, loadNotifications])

  const refreshRef = useRef(refresh)
  useEffect(() => {
    refreshRef.current = refresh
  }, [refresh])

  useEffect(() => {
    loadNotifications()
    // onReady covers the initial connect and every reconnect, catching
    // anything missed while disconnected.
    return subscribeEvents(
      () => refreshRef.current(),
      () => refreshRef.current(),
    )
  }, [loadNotifications])

  const loadMore = useCallback(() => {
    const last = missions[missions.length - 1]
    if (!last || loadingMore.current) return
    loadingMore.current = true
    const g = gen.current
    listMissions({ ...query, cursor: { before: last.created_at, beforeId: last.id } })
      .then((page) => {
        if (g !== gen.current) return
        noteSeen(page)
        setBoard((prev) => {
          const seen = new Set(prev.missions.map((m) => m.id))
          return {
            missions: [...prev.missions, ...page.filter((m) => !seen.has(m.id))],
            hasMore: page.length === missionsPageSize,
          }
        })
      }, () => undefined)
      .finally(() => {
        if (g === gen.current) loadingMore.current = false
      })
  }, [missions, query, noteSeen])

  // Infinite scroll: fetch the next page when the sentinel below the
  // grid scrolls into view.
  useEffect(() => {
    const el = sentinelRef.current
    if (!el || !board.hasMore) return
    const obs = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting)) loadMore()
    })
    obs.observe(el)
    return () => obs.disconnect()
  }, [board.hasMore, loadMore])

  const unread = notifications.filter((n) => !n.read)

  const dismiss = (id: string) => {
    markNotificationRead(id).then(loadNotifications, () => undefined)
  }

  const filtersActive =
    kindFilter !== 'all' || harnessFilter !== 'all' || modelFilter !== 'all' || sourceFilter !== 'all'

  return (
    <PageShell>
      <PageHeader
        title="Missions"
        description="Long-running tasks that plan, execute, and review their own work."
        actions={
          <Button data-tour="missions.new" onClick={() => navigate('/missions/new')}>
            New mission
          </Button>
        }
      />

      {unread.length > 0 && (
        <div className="mb-6 space-y-2">
          {unread.map((n) => (
            <Alert key={n.id} tone={notificationTone(n.kind)} className="flex items-center justify-between gap-3">
              {n.mission_id ? (
                <button
                  type="button"
                  onClick={() => navigate(`/missions/${n.mission_id}`)}
                  className="flex items-center gap-2 text-left hover:underline"
                >
                  <Bell aria-hidden className="size-4 shrink-0" />
                  {n.message}
                </button>
              ) : (
                <span className="flex items-center gap-2">
                  <Bell aria-hidden className="size-4 shrink-0" />
                  {n.message}
                </span>
              )}
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

      <div data-tour="missions.filters" className="mb-4 flex flex-wrap items-center gap-2">
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
      </div>

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {missions.map((m) => (
          <MissionCard key={m.id} mission={m} />
        ))}
        {missions.length === 0 && (
          <div className="col-span-full rounded-md border border-dashed border-border">
            <EmptyState
              icon={Inbox}
              title={filtersActive ? 'No missions match the current filters.' : 'No missions yet'}
              description={
                filtersActive
                  ? undefined
                  : 'A mission is a longer task Timothy works on by itself and reports back.'
              }
              action={
                filtersActive ? undefined : (
                  <Button onClick={() => navigate('/missions/new')}>New mission</Button>
                )
              }
            />
          </div>
        )}
      </div>
      {board.hasMore && <div ref={sentinelRef} className="h-px" data-testid="missions-sentinel" />}
      <TourOverlay {...tour} />
    </PageShell>
  )
}
