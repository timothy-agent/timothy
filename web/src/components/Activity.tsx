import { WrenchIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { Badge } from './ui/badge'
import { SheetContent, SheetHeader, SheetTitle } from './ui/sheet'
import { CopyButton } from './Message'
import type { AssistantState, ToolRun } from '../lib/chat'
import { formatDuration } from '../lib/format'

// prettyArgs pretty-prints a tool call's JSON args; malformed or
// non-JSON input renders raw rather than throwing.
export function prettyArgs(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

// totalDuration sums the wall time of every tool call that reported
// one. Calls still running (no durationMs yet) contribute nothing —
// an honest "0" rather than a guess is the caller's job to phrase.
export function totalDuration(tools: ToolRun[]): number {
  return tools.reduce((sum, t) => sum + (t.durationMs ?? 0), 0)
}

// summarizeTools names what a turn did, deduped by first appearance
// and capped so a turn with a dozen tool calls still reads as one
// line: "search_web, 2× fetch_url, +2 more".
export function summarizeTools(tools: ToolRun[]): string {
  const order: string[] = []
  const counts = new Map<string, number>()
  for (const t of tools) {
    if (!counts.has(t.name)) order.push(t.name)
    counts.set(t.name, (counts.get(t.name) ?? 0) + 1)
  }
  const names = order.map((name) => {
    const n = counts.get(name) ?? 1
    return n > 1 ? `${n}× ${name}` : name
  })
  if (names.length <= 3) return names.join(', ')
  return `${names.slice(0, 3).join(', ')}, +${names.length - 3} more`
}

const statusStyles: Record<ToolRun['status'], string> = {
  running: 'border-blue-500/40 bg-blue-500/10 text-blue-600 dark:text-blue-400',
  ok: 'border-emerald-500/40 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
  error: 'border-red-500/40 bg-red-500/10 text-red-600 dark:text-red-400',
  denied: 'border-amber-500/40 bg-amber-500/10 text-amber-600 dark:text-amber-400',
}

// ActivityLine is the collapsed, inline summary of a turn's reasoning
// and tool calls — a thin button, not a card, that opens the detail
// panel. It renders only when there is something to summarize.
export function ActivityLine({ msg, onOpen }: { msg: AssistantState; onOpen: () => void }) {
  if (msg.tools.length === 0 && msg.reasoning === '') return null

  const hasError = msg.tools.some((t) => t.status === 'error')
  const hasDenied = msg.tools.some((t) => t.status === 'denied')
  const running = msg.tools.find((t) => t.status === 'running')

  let label: string
  if (msg.streaming && running) label = `Running ${running.name}…`
  else if (msg.streaming) label = 'Thinking…'
  else if (msg.tools.length > 0) {
    const sum = totalDuration(msg.tools)
    const prefix =
      sum > 0 ? `Worked for ${formatDuration(sum)}` : `Ran ${msg.tools.length} tools`
    label = `${prefix} · ${summarizeTools(msg.tools)}`
  } else label = 'Reasoning'

  return (
    <button
      type="button"
      onClick={onOpen}
      data-testid="activity-line"
      className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground"
    >
      {msg.streaming && (
        <span className="size-1.5 animate-pulse rounded-full bg-blue-500" aria-hidden />
      )}
      {hasError && <span className="size-1.5 rounded-full bg-red-500" aria-hidden />}
      {!hasError && hasDenied && <span className="size-1.5 rounded-full bg-amber-500" aria-hidden />}
      {label}
    </button>
  )
}

// ToolCallRow renders one call's full detail: name, status, duration,
// pretty-printed args, and the result digest — each copyable. Raw
// tool results never reach the browser; the digest is all there is.
function ToolCallRow({ tool }: { tool: ToolRun }) {
  return (
    <div className="space-y-2 rounded-lg border border-zinc-200 p-3 text-xs dark:border-zinc-700">
      <div className="flex items-center gap-2">
        <HugeiconsIcon icon={WrenchIcon} className="size-3.5 text-zinc-400" />
        <span className="font-mono text-zinc-600 dark:text-zinc-300">{tool.name}</span>
        <Badge variant="outline" className={statusStyles[tool.status]} data-testid="tool-status">
          {tool.status === 'running' ? 'running…' : tool.status}
        </Badge>
        {tool.durationMs !== undefined && (
          <span className="text-zinc-400">{formatDuration(tool.durationMs)}</span>
        )}
      </div>
      {tool.args && (
        <div className="relative">
          <pre className="overflow-x-auto rounded bg-zinc-100 p-2 pr-8 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300">
            {prettyArgs(tool.args)}
          </pre>
          <div className="absolute top-1 right-1">
            <CopyButton text={tool.args} label="Copy tool args" alwaysVisible />
          </div>
        </div>
      )}
      {tool.digest && (
        <div className="relative">
          <pre className="max-h-64 overflow-auto rounded bg-zinc-100 p-2 pr-8 whitespace-pre-wrap text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300">
            {tool.digest}
          </pre>
          <div className="absolute top-1 right-1">
            <CopyButton text={tool.digest} label="Copy tool response" alwaysVisible />
          </div>
        </div>
      )}
    </div>
  )
}

// ActivityPanel is the full detail behind an ActivityLine: reasoning
// text plus every tool call, args and digest included. Chat.tsx hosts
// one Sheet instance and re-derives the item from live state each
// render, so the panel updates in real time while a turn streams.
export function ActivityPanel({ msg }: { msg: AssistantState }) {
  const sum = totalDuration(msg.tools)
  return (
    <SheetContent side="right" className="data-[side=right]:sm:max-w-lg">
      <SheetHeader>
        <SheetTitle>Activity</SheetTitle>
        {msg.tools.length > 0 && (
          <p className="text-sm text-muted-foreground">
            {msg.tools.length} tool{msg.tools.length === 1 ? '' : 's'}
            {sum > 0 && ` · ${formatDuration(sum)}`}
          </p>
        )}
      </SheetHeader>
      <div className="flex-1 space-y-3 overflow-y-auto px-4 pb-4">
        {msg.reasoning !== '' && (
          <div className="relative space-y-1">
            <div className="flex items-center justify-between">
              <span className="text-xs font-medium text-muted-foreground">Reasoning</span>
              <CopyButton text={msg.reasoning} label="Copy reasoning" alwaysVisible />
            </div>
            <div className="rounded-lg border border-zinc-200 p-3 text-xs whitespace-pre-wrap text-zinc-500 dark:border-zinc-700 dark:text-zinc-400">
              {msg.reasoning}
            </div>
          </div>
        )}
        {msg.tools.map((t) => (
          <ToolCallRow key={t.id} tool={t} />
        ))}
      </div>
    </SheetContent>
  )
}
