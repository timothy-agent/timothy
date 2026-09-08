import { useMemo, useRef } from 'react'
import { ArrowDown, ArrowUp } from 'lucide-react'
import type { MissionEvent, MissionKBHit, MissionToolCallPayload } from '../../api/types'
import { Panel } from '../timothy/panel'
import { CopyButton } from '../timothy/copy-button'
import { IconButton } from '../timothy/icon-button'
import { EventLog, type EventLogRow } from '../timothy/event-log'
import { ToolCallCard } from '../chat/ToolCallCard'
import { phaseLabel, phaseTextColors } from '../../lib/phaseColors'
import { eventIcon, renderEvent, toolRunFromEvent } from './eventRenderers'
import { FullscreenDialog, FullscreenToggle, useFullscreenPanel } from './FullscreenPanel'
import { TooltipProvider } from '../ui/tooltip'

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

// rowPhases maps each rendered row's seq to the phase it should be
// prefixed with. A mission never emits phase_started for its INITIAL
// phase (only transitions), so anchoring on the first phase_started
// would mislabel every earlier row with the first TRANSITION's phase.
// Most events carry their own payload.phase (turn, tool_call, input
// and permission events); that is authoritative for the row and
// advances the running phase, with phase_started marking transitions
// for the payload-less rows between them. Leading rows before any
// signal (e.g. provisioned) backfill from the first known phase. A
// mission with no phase signal at all leaves every row unlabeled.
function rowPhases(rows: MissionEvent[]): Map<number, string> {
  const phases = new Map<number, string>()
  let current = ''
  for (const e of rows) {
    // payload can be null (e.g. mission.resumed), never assume an object
    const own = String(((e.payload ?? {}) as { phase?: string }).phase ?? '')
    if (own !== '') {
      current = own
    }
    phases.set(e.seq, current)
  }
  const first = rows.map((e) => phases.get(e.seq) ?? '').find((p) => p !== '')
  if (first === undefined) return new Map()
  for (const e of rows) {
    if (phases.get(e.seq) === '') phases.set(e.seq, first)
    else break
  }
  return phases
}

// KBHitList renders a search_kb call's returned hits (issue #413):
// document title/id and fused score, or an explicit "no hits" line for
// an empty result: a bare-blank block there would be indistinguishable
// from a search that simply hasn't finished rendering.
function KBHitList({ hits }: { hits: MissionKBHit[] }) {
  if (hits.length === 0) {
    return <div className="mt-0.5 rounded-md bg-muted px-2 py-1 text-xs text-muted-foreground">no hits</div>
  }
  return (
    <ol className="mt-0.5 space-y-0.5 rounded-md bg-muted px-2 py-1 text-xs text-muted-foreground">
      {hits.map((h) => (
        <li key={h.document_id} className="truncate">
          {h.document_title || h.document_id} · score {h.score.toFixed(4)}
        </li>
      ))}
    </ol>
  )
}

// MissionToolCallCard renders one mission.tool_call event as the
// shared chat ToolCallCard (issue #587): collapsed shows the
// humanized action (e.g. "Search kb"), expanded shows the raw tool
// name in mono plus arguments/digest, so the machine-readable
// identifier stays discoverable without being the headline label. A
// search_kb call's KB hits render as extra expanded content (issue
// #413): the point of expanding is seeing the hits, not another
// disclosure to click through.
function MissionToolCallCard({ event }: { event: MissionEvent }) {
  const run = toolRunFromEvent(event)
  const { kb_hits } = event.payload as MissionToolCallPayload
  return <ToolCallCard run={run}>{kb_hits && <KBHitList hits={kb_hits} />}</ToolCallCard>
}

// timelineText renders a plain-text version of the timeline for the
// copy button: one line per row, timestamp plus event kind (the
// short, stable label; renderEvent's JSX detail is skipped, it's
// styled markup, not plain text).
function timelineText(rows: MissionEvent[]): string {
  return rows.map((e) => `${new Date(e.created_at).toLocaleString()} · ${e.kind}`).join('\n')
}

export function TimelineSection({ events }: { events: MissionEvent[] }) {
  const rows = useMemo(() => events.filter(rowKind), [events])
  const toolCallsByTurn = useMemo(() => turnToolCalls(events), [events])
  const phasesBySeq = useMemo(() => rowPhases(rows), [rows])
  const scrollRef = useRef<HTMLDivElement>(null)
  const { fullscreen, toggle, close } = useFullscreenPanel()

  const logRows: EventLogRow[] = rows.map((e) => {
    const calls = e.kind === 'mission.turn' ? (toolCallsByTurn.get(e.seq) ?? []) : []
    const phase = phasesBySeq.get(e.seq)
    const title = (
      <>
        {renderEvent(e, rows)}
        {calls.length > 0 && (
          <span className="text-muted-foreground"> · {calls.length} tool call{calls.length === 1 ? '' : 's'}</span>
        )}
      </>
    )
    const payload = disclosurePayloadKinds.has(e.kind) ? e.payload : undefined
    return {
      id: String(e.seq),
      time: new Date(e.created_at),
      kind: e.kind,
      icon: eventIcon(e.kind),
      label: phase ? (
        <span className={phaseTextColors[phaseLabel(phase)] ?? 'text-muted-foreground'}>{phaseLabel(phase)}</span>
      ) : undefined,
      title,
      payload,
      children:
        calls.length > 0 ? (
          <div className="space-y-1">
            {calls.map((c) => (
              <MissionToolCallCard key={c.seq} event={c} />
            ))}
          </div>
        ) : undefined,
    }
  })

  const scrollToTop = () => {
    scrollRef.current?.scrollTo({ top: 0, behavior: 'smooth' })
  }
  const scrollToBottom = () => {
    const el = scrollRef.current
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
  }

  const toolbar = (
    <>
      <IconButton label="Scroll to top" icon={ArrowUp} size="xs" variant="ghost" onClick={scrollToTop} />
      <IconButton label="Scroll to bottom" icon={ArrowDown} size="xs" variant="ghost" onClick={scrollToBottom} />
    </>
  )

  const log = (
    <div className={fullscreen ? 'flex min-h-0 flex-1' : undefined}>
      <EventLog
        rows={logRows}
        scrollRef={scrollRef}
        className={fullscreen ? 'min-h-0 flex-1' : undefined}
        fill={fullscreen}
        ariaLabel="Mission timeline"
        toolbar={toolbar}
      />
    </div>
  )

  const actions = (
    <>
      <CopyButton value={timelineText(rows)} label="Copy timeline" />
      <FullscreenToggle fullscreen={fullscreen} onToggle={toggle} />
    </>
  )

  const panel = (
    <Panel
      title="Timeline"
      density="operational"
      actions={actions}
      className={fullscreen ? 'flex h-full min-h-0 flex-col' : undefined}
      bodyClassName={fullscreen ? 'flex min-h-0 flex-1 flex-col' : undefined}
    >
      {log}
    </Panel>
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

// disclosurePayloadKinds are the kinds whose own renderer output only
// summarizes the payload: the row gets a disclosure with the raw
// payload underneath. Everything else's renderer already shows every
// field it has, so no extra disclosure.
const disclosurePayloadKinds = new Set([
  'mission.plan_created',
  'mission.review_verdict',
  'mission.blocked',
  'mission.failed',
  'mission.violation',
  'mission.recovery',
  'executor.result',
  'mission.permission_requested',
  'mission.steered',
  'mission.route_changed',
])
