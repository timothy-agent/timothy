import { useState, type ReactNode } from 'react'

import { traceIcons, TraceGroup, TraceRow } from '@/components/timothy/trace-group'
import { toolCallStatus } from '@/components/timothy/status'
import { JsonBlock } from '@/components/timothy/json-block'
import { Button } from '@/components/ui/button'
import { formatDuration } from '@/lib/format'
import { totalDuration } from '@/lib/activity'
import { toolCategory, humanizeTool } from '@/lib/chatUi'
import type { ToolRun } from '@/lib/chat'

function parseArgs(args?: string): unknown {
  if (!args) return undefined
  try {
    return JSON.parse(args)
  } catch {
    return args
  }
}

const DIGEST_LINES = 20

function DigestBlock({ digest, isError }: { digest: string; isError: boolean }) {
  const [expanded, setExpanded] = useState(false)
  if (isError) {
    return (
      <pre className="whitespace-pre-wrap break-words rounded-md bg-destructive-soft p-2 font-mono text-trace text-destructive">
        {digest}
      </pre>
    )
  }
  const lines = digest.split('\n')
  const truncated = lines.length > DIGEST_LINES
  const shown = expanded || !truncated ? digest : lines.slice(0, DIGEST_LINES).join('\n')
  return (
    <div className="min-w-0">
      <pre className="whitespace-pre-wrap break-words font-mono text-xs leading-[18px] text-muted-foreground">{shown}</pre>
      {truncated && (
        <Button variant="ghost" size="xs" onClick={() => setExpanded((v) => !v)}>
          {expanded ? 'Show less' : `Show all (+${lines.length - DIGEST_LINES} lines)`}
        </Button>
      )}
    </div>
  )
}

// ToolCallCard renders one tool call as an operational trace row,
// collapsed by default, expanding to arguments and result (14.5).
// Note: TraceRow's own Collapsible has no defaultOpen passthrough
// (only TraceGroup does), so this component's defaultOpen only
// affects ToolCallGroup's outer group open state, not an individual
// card's expansion.
export function ToolCallCard({
  run,
  density = 'trace',
  children,
}: {
  run: ToolRun
  density?: 'trace' | 'operational'
  defaultOpen?: boolean
  // Extra expanded content below the digest (e.g. a search_kb hit list).
  children?: ReactNode
}) {
  const { action, target } = humanizeTool(run)
  const status = toolCallStatus(run.status)
  const icon = traceIcons[toolCategory(run.name)]
  const duration = run.durationMs !== undefined ? formatDuration(run.durationMs) : undefined
  const args = parseArgs(run.args)
  const isError = run.status === 'error'

  const hasDetail =
    args !== undefined ||
    run.digest !== undefined ||
    run.status === 'denied' ||
    run.status === 'canceled' ||
    children !== undefined
  if (!hasDetail) {
    return (
      <div className="min-w-0 w-full" data-density={density}>
        <TraceRow status={status} icon={icon} action={action} target={target} duration={duration} />
      </div>
    )
  }

  return (
    <div className="min-w-0 w-full" data-density={density}>
      <TraceRow status={status} icon={icon} action={action} target={target} duration={duration}>
        <div className="min-w-0 space-y-2">
          {/* raw tool identifier, kept discoverable alongside the humanized action */}
          <p className="font-mono text-trace text-muted-foreground">{run.name}</p>
          {args !== undefined && <JsonBlock value={args} density="trace" label="Arguments" maxLines={20} />}
          {run.status === 'denied' && <p className="text-sm text-muted-foreground">Denied by you</p>}
          {run.status === 'canceled' && <p className="text-sm text-muted-foreground">Stopped</p>}
          {run.digest !== undefined && <DigestBlock digest={run.digest} isError={isError} />}
          {children}
        </div>
      </TraceRow>
    </div>
  )
}

// ToolCallGroup folds several tool calls into one group row (14.5); a
// single call renders as a plain ToolCallCard with no group chrome,
// wrapped in the same recess TraceGroup applies so single and
// multiple calls sit at identical indentation.
export function ToolCallGroup({ runs, defaultOpen = false }: { runs: ToolRun[]; defaultOpen?: boolean }) {
  if (runs.length === 1)
    return (
      <div className="ml-1 border-l-2 border-border pl-3">
        <ToolCallCard run={runs[0]} defaultOpen={defaultOpen} />
      </div>
    )

  const anyRunning = runs.some((r) => r.status === 'running')
  return (
    <TraceGroup
      summary={`${runs.length} tool calls`}
      duration={formatDuration(totalDuration(runs))}
      defaultOpen={defaultOpen || anyRunning}
    >
      {runs.map((run) => (
        <ToolCallCard key={run.id} run={run} />
      ))}
    </TraceGroup>
  )
}
