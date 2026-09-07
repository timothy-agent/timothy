import { Fragment, useEffect, useRef, useState, type ReactNode, type RefObject } from 'react'
import { ChevronRight, Pause, Play, type LucideIcon } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'
import { JsonBlock } from './json-block'
import { StatusDot } from './status-dot'
import { statusMeta, type Status } from './status'
import { traceIcons } from './trace-group'

// How close to the bottom (px) counts as "already following the
// tail": matches TimelineSection's own threshold so a reader who has
// scrolled up to read history never gets yanked back down.
const followThresholdPx = 48

export interface EventLogRow {
  id: string
  time: Date
  kind: string
  icon?: LucideIcon
  status?: Status
  title: ReactNode
  payload?: unknown
  payloadNode?: ReactNode
  children?: ReactNode
}

function formatTime(d: Date): string {
  return d.toLocaleTimeString(undefined, { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function formatDay(d: Date): string {
  return d.toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric' })
}

function isSameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}

function DayDivider({ date }: { date: Date }) {
  return (
    <li role="presentation" className="bg-muted/40 px-3 py-1 text-xs text-muted-foreground">
      {formatDay(date)}
    </li>
  )
}

function EventLogRowItem({ row }: { row: EventLogRow }) {
  const Icon = row.icon ?? traceIcons.other
  const hasDisclosure = row.payload !== undefined || row.payloadNode !== undefined || row.children !== undefined

  const head = (
    <>
      <time dateTime={row.time.toISOString()} className="w-16 shrink-0 font-mono text-trace text-muted-foreground tabular-nums">
        {formatTime(row.time)}
      </time>
      {row.status && <StatusDot status={row.status} label={statusMeta[row.status].label} size="sm" />}
      <Icon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
      <span className="min-w-0 flex-1 break-words leading-5 [overflow-wrap:anywhere]">{row.title}</span>
    </>
  )

  if (!hasDisclosure) {
    return (
      <li className="flex min-h-8 items-start gap-2 px-3 py-1.5 text-sm">
        {head}
      </li>
    )
  }

  return (
    <li className="min-h-8 px-3 py-1.5 text-sm">
      <Collapsible>
        <CollapsibleTrigger className="flex w-full items-start gap-2 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background [&[data-state=open]_svg[data-chevron]]:rotate-90">
          <ChevronRight data-chevron aria-hidden className="mt-[3px] size-3.5 shrink-0 text-muted-foreground transition-transform duration-100" />
          {head}
        </CollapsibleTrigger>
        <CollapsibleContent className="pt-1.5 pl-6">
          {row.payloadNode ?? (row.payload !== undefined && <JsonBlock value={row.payload} density="trace" maxLines={20} />)}
          {row.children}
        </CollapsibleContent>
      </Collapsible>
    </li>
  )
}

// EventLog is the canonical operational event log (contract 14.11):
// the mission timeline and the chat activity trace are one component.
export function EventLog({
  rows,
  follow,
  onFollowChange,
  emptyText = 'No events yet.',
  className,
  ariaLabel = 'Event log',
  toolbar,
  scrollRef,
}: {
  rows: EventLogRow[]
  follow?: boolean
  onFollowChange?: (v: boolean) => void
  emptyText?: string
  className?: string
  ariaLabel?: string
  toolbar?: ReactNode
  // Optional external ref to the scroll container, for a caller that
  // needs to drive scrolling itself (e.g. scroll-to-top/bottom controls).
  scrollRef?: RefObject<HTMLDivElement | null>
}) {
  const [internalFollow, setInternalFollow] = useState(true)
  const following = follow ?? internalFollow
  const ownRef = useRef<HTMLDivElement>(null)
  const containerRef = scrollRef ?? ownRef
  // Row count at the moment following was last turned off, so "N new"
  // is always rows since pausing, not a running total across pauses.
  const [pausedAtCount, setPausedAtCount] = useState(rows.length)

  const setFollowing = (v: boolean) => {
    if (!v) setPausedAtCount(rows.length)
    if (onFollowChange) onFollowChange(v)
    else setInternalFollow(v)
  }

  useEffect(() => {
    const el = containerRef.current
    if (following && el) el.scrollTop = el.scrollHeight
  }, [rows.length, following, containerRef])

  const newSinceCount = following ? 0 : Math.max(0, rows.length - pausedAtCount)

  const handleScroll = () => {
    const el = containerRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight <= followThresholdPx
    if (atBottom !== following) setFollowing(atBottom)
  }

  const jumpToBottom = () => {
    const el = containerRef.current
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
    setFollowing(true)
  }

  // showDivider[i] is true when row i starts a new calendar day: the
  // first row shows one only if the whole list spans more than one
  // day, every later row compares against the previous row's day.
  const showDivider = rows.map((row, i) => {
    if (i === 0) return rows.length > 0 && !isSameDay(rows[0].time, rows[rows.length - 1].time)
    return !isSameDay(rows[i - 1].time, row.time)
  })

  return (
    <div className={cn('flex flex-col', className)}>
      <div className="flex h-9 items-center gap-2 border-b border-border px-3">
        <span className="text-xs text-muted-foreground">
          {rows.length} event{rows.length === 1 ? '' : 's'}
        </span>
        <div className="ml-auto flex items-center gap-2">
          <Button
            variant="ghost"
            size="xs"
            aria-pressed={following}
            onClick={() => setFollowing(!following)}
          >
            {following ? <Pause aria-hidden className="size-3.5" /> : <Play aria-hidden className="size-3.5" />}
            {following ? 'Pause' : 'Follow'}
          </Button>
          {!following && newSinceCount > 0 && (
            <Badge variant="info" size="sm" asChild>
              <button type="button" onClick={jumpToBottom}>
                {newSinceCount} new
              </button>
            </Badge>
          )}
          {toolbar}
        </div>
      </div>
      <div
        ref={containerRef}
        onScroll={handleScroll}
        role="log"
        aria-live={following ? 'polite' : 'off'}
        aria-label={ariaLabel}
        tabIndex={0}
        className="h-80 min-h-80 resize-y overflow-y-auto focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
      >
        {rows.length === 0 ? (
          <p className="px-3 py-2 text-sm text-muted-foreground">{emptyText}</p>
        ) : (
          <ol className="divide-y divide-border">
            {rows.map((row, i) => (
              <Fragment key={row.id}>
                {showDivider[i] && <DayDivider date={row.time} />}
                <EventLogRowItem row={row} />
              </Fragment>
            ))}
          </ol>
        )}
      </div>
    </div>
  )
}
