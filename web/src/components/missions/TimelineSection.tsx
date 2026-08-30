import { ArrowDown01Icon, ArrowRight01Icon, ArrowUp01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useRef, useState } from 'react'
import type { MissionEvent, MissionKBHit, MissionToolCallPayload } from '../../api/types'
import { CopyButton } from '../Message'
import { Button } from '../ui/button'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '../ui/tooltip'
import { renderEvent, toolCallStatusClass } from './eventRenderers'
import { FullscreenDialog, FullscreenToggle, useFullscreenPanel } from './FullscreenPanel'
import { formatDuration } from '../../lib/format'

// How close to the bottom (px) counts as "already following the tail"
//: auto-scroll only kicks in within this margin, so a reader who has
// scrolled up to read history never gets yanked back down by new
// events arriving from the poll loop.
const followThresholdPx = 48

// executor.progress fires on every byte the delegated CLI executor
// writes: rendering one row per event would flood the timeline, so
// it's excluded here; MissionDetail's phase header shows the latest
// one instead as a lightweight live indicator. mission.tool_call is
// excluded the same way: it renders nested under its owning
// mission.turn row (see turnToolCalls) instead of as its own Timeline
// line.
const rowKind = (e: MissionEvent) => e.kind !== 'executor.progress' && e.kind !== 'mission.tool_call'

// turnToolCalls groups every mission.tool_call event by the
// mission.turn row it belongs to: a turn's tool calls are the
// mission.tool_call events between the PRECEDING mission.turn event
// (exclusive) and this one (inclusive), in seq order: matching how
// runner.go's runTurn appends one mission.tool_call per finished call
// during the turn, then driver.go's Advance appends the mission.turn
// summary once the turn (and any recovery re-run) completes. Keyed by
// the mission.turn event's own seq, since that's a stable per-row key
// already used elsewhere in this file.
function turnToolCalls(events: MissionEvent[]): Map<number, MissionEvent[]> {
  const byTurn = new Map<number, MissionEvent[]>()
  let pending: MissionEvent[] = []
  for (const e of events) {
    if (e.kind === 'mission.tool_call') {
      pending.push(e)
    } else if (e.kind === 'mission.turn') {
      byTurn.set(e.seq, pending)
      pending = []
    }
  }
  return byTurn
}

// TurnTraceToggle renders the expand/collapse control and, when
// expanded, the ordered tool-call trace for one mission.turn row.
function TurnTraceToggle({ calls }: { calls: MissionEvent[] }) {
  const [open, setOpen] = useState(false)
  if (calls.length === 0) return null
  return (
    <div className="mt-0.5">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="inline-flex items-center gap-1 text-zinc-500 hover:text-zinc-300"
      >
        <HugeiconsIcon icon={ArrowRight01Icon} className={`size-3 transition-transform ${open ? 'rotate-90' : ''}`} />
        {calls.length} tool call{calls.length === 1 ? '' : 's'}
      </button>
      {open && (
        <ol className="mt-1 space-y-0.5 border-l border-zinc-800 pl-3">
          {calls.map((c) => {
            const { tool, status, duration_ms, args_digest, kb_hits } = c.payload as MissionToolCallPayload
            return (
              <li key={c.seq} className="text-zinc-400">
                <span className={toolCallStatusClass(status)}>{tool}</span> · {status} ·{' '}
                {formatDuration(duration_ms)}
                {args_digest && (
                  <code className="ml-1 block truncate rounded bg-muted/20 px-1 py-0.5 text-[11px] text-zinc-500">
                    {args_digest}
                  </code>
                )}
                {kb_hits && <KBHitList hits={kb_hits} />}
              </li>
            )
          })}
        </ol>
      )}
    </div>
  )
}

// KBHitList renders a search_kb call's returned hits (issue #413):
// document title/id and fused score, or an explicit "no hits" line for
// an empty result: a bare-blank block there would be indistinguishable
// from a search that simply hasn't finished rendering.
function KBHitList({ hits }: { hits: MissionKBHit[] }) {
  if (hits.length === 0) {
    return <div className="ml-1 mt-0.5 rounded bg-muted/20 px-1 py-0.5 text-[11px] text-zinc-500">no hits</div>
  }
  return (
    <ol className="ml-1 mt-0.5 space-y-0.5 rounded bg-muted/20 px-1 py-0.5 text-[11px] text-zinc-500">
      {hits.map((h) => (
        <li key={h.document_id} className="truncate">
          {h.document_title || h.document_id} · score {h.score.toFixed(4)}
        </li>
      ))}
    </ol>
  )
}

// timelineText renders a plain-text version of the timeline for the
// copy button: one line per row, timestamp plus event kind (the
// short, stable label; renderEvent's JSX detail is skipped, it's
// styled markup, not plain text).
function timelineText(rows: MissionEvent[]): string {
  return rows.map((e) => `${new Date(e.created_at).toLocaleString()} · ${e.kind}`).join('\n')
}

export function TimelineSection({ events }: { events: MissionEvent[] }) {
  const rows = events.filter(rowKind)
  const toolCallsByTurn = turnToolCalls(events)
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
        <div className="flex items-center gap-1">
          <Tooltip>
            <TooltipTrigger asChild>
              <Button variant="ghost" size="icon-xs" aria-label="Scroll to top" onClick={scrollToTop}>
                <HugeiconsIcon icon={ArrowUp01Icon} />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Scroll to top</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button variant="ghost" size="icon-xs" aria-label="Scroll to bottom" onClick={scrollToBottom}>
                <HugeiconsIcon icon={ArrowDown01Icon} />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Scroll to bottom</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <span className="inline-flex">
                <CopyButton text={timelineText(rows)} label="Copy timeline" alwaysVisible />
              </span>
            </TooltipTrigger>
            <TooltipContent>Copy</TooltipContent>
          </Tooltip>
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
                <span className="flex-1 break-words">
                  {renderEvent(e, rows)}
                  {e.kind === 'mission.turn' && <TurnTraceToggle calls={toolCallsByTurn.get(e.seq) ?? []} />}
                </span>
              </li>
            ))}
          </ol>
        )}
      </div>
    </div>
  )

  if (!fullscreen) return <TooltipProvider>{panel}</TooltipProvider>
  return (
    <TooltipProvider>
      <FullscreenDialog open={fullscreen} onOpenChange={(o) => !o && close()}>
        {panel}
      </FullscreenDialog>
    </TooltipProvider>
  )
}
