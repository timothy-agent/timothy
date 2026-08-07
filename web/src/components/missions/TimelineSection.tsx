import { ArrowDown01Icon, ArrowUp01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useRef } from 'react'
import type { MissionEvent } from '../../api/types'
import { Button } from '../ui/button'
import { renderEvent } from './eventRenderers'
import { FullscreenDialog, FullscreenToggle, useFullscreenPanel } from './FullscreenPanel'

// How close to the bottom (px) counts as "already following the tail"
// — auto-scroll only kicks in within this margin, so a reader who has
// scrolled up to read history never gets yanked back down by new
// events arriving from the poll loop.
const followThresholdPx = 48

// executor.progress fires on every byte the delegated CLI executor
// writes — rendering one row per event would flood the timeline, so
// it's excluded here; MissionDetail's phase header shows the latest
// one instead as a lightweight live indicator.
const rowKind = (e: MissionEvent) => e.kind !== 'executor.progress'

export function TimelineSection({ events }: { events: MissionEvent[] }) {
  const rows = events.filter(rowKind)
  const containerRef = useRef<HTMLDivElement>(null)
  const wasAtBottomRef = useRef(true)
  const { fullscreen, toggle, close } = useFullscreenPanel()

  useEffect(() => {
    const el = containerRef.current
    if (el && wasAtBottomRef.current) {
      el.scrollTop = el.scrollHeight
    }
  }, [rows])

  const handleScroll = () => {
    const el = containerRef.current
    if (!el) return
    wasAtBottomRef.current = el.scrollHeight - el.scrollTop - el.clientHeight <= followThresholdPx
  }

  const scrollToTop = () => {
    containerRef.current?.scrollTo({ top: 0, behavior: 'smooth' })
    wasAtBottomRef.current = false
  }

  const scrollToBottom = () => {
    const el = containerRef.current
    if (!el) return
    el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
    wasAtBottomRef.current = true
  }

  const panel = (
    <div
      className={
        fullscreen
          ? 'flex h-full flex-col overflow-hidden rounded-lg border border-border'
          : 'overflow-hidden rounded-lg border border-border'
      }
    >
      <div className="flex items-center justify-between border-b border-border bg-muted/50 px-3 py-1.5">
        <span className="text-xs text-muted-foreground">
          {rows.length} event{rows.length === 1 ? '' : 's'}
        </span>
        <div className="flex gap-1">
          <Button variant="ghost" size="icon-xs" title="Scroll to top" onClick={scrollToTop}>
            <HugeiconsIcon icon={ArrowUp01Icon} />
          </Button>
          <Button variant="ghost" size="icon-xs" title="Scroll to bottom" onClick={scrollToBottom}>
            <HugeiconsIcon icon={ArrowDown01Icon} />
          </Button>
          <FullscreenToggle fullscreen={fullscreen} onToggle={toggle} />
        </div>
      </div>
      <div
        ref={containerRef}
        onScroll={handleScroll}
        className={
          fullscreen
            ? 'flex-1 overflow-y-auto bg-zinc-950 px-3 py-2 font-mono text-xs dark:bg-black'
            : 'h-80 overflow-y-auto bg-zinc-950 px-3 py-2 font-mono text-xs dark:bg-black'
        }
      >
        {rows.length === 0 ? (
          <p className="text-zinc-500">No events yet.</p>
        ) : (
          <ol className="space-y-1">
            {rows.map((e) => (
              <li key={e.seq} className="flex gap-3 text-zinc-300">
                <span className="w-24 shrink-0 whitespace-nowrap text-zinc-500">
                  {new Date(e.created_at).toLocaleTimeString()}
                </span>
                <span className="flex-1 break-words">{renderEvent(e, rows)}</span>
              </li>
            ))}
          </ol>
        )}
      </div>
    </div>
  )

  if (!fullscreen) return panel
  return (
    <FullscreenDialog open={fullscreen} onOpenChange={(o) => !o && close()}>
      {panel}
    </FullscreenDialog>
  )
}
