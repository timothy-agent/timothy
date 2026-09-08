import { Hand, CircleX, CircleCheck } from 'lucide-react'

import { Spinner } from '@/components/timothy/spinner'
import { statusText } from '@/components/timothy/status'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { formatDuration } from '@/lib/format'

// humanizeToolName turns a raw tool name into its display label:
// underscores/hyphens to spaces (e.g. "search_web" -> "search web").
function humanizeToolName(name: string): string {
  return name.replace(/[_-]/g, ' ')
}

// AgentPhase is the reduced view of a turn's live state that the
// status line renders (design contract section 14.3).
export type AgentPhase =
  | { kind: 'idle' }
  | { kind: 'thinking' }
  | { kind: 'tool'; tool: string }
  | { kind: 'streaming' }
  | { kind: 'waiting'; what: 'approval' | 'answer'; onFocusRequest?: () => void }
  | { kind: 'error'; message: string; onRetry?: () => void }
  | { kind: 'done'; durationMs?: number; toolCount: number }

function doneCopy(durationMs: number | undefined, toolCount: number): string {
  const duration = durationMs === undefined ? undefined : formatDuration(durationMs)
  const base = duration === undefined ? 'Worked' : `Worked for ${duration}`
  if (toolCount === 0) return base
  const tools = toolCount === 1 ? '1 tool' : `${toolCount} tools`
  return `${base} · ${tools}`
}

// AgentStatusLine is the single source of "what is Timothy doing"
// (section 14.3): one role="status" live region, always mounted.
export function AgentStatusLine({ phase, className }: { phase: AgentPhase; className?: string }) {
  const content = (() => {
    switch (phase.kind) {
      case 'idle':
        return null
      case 'thinking':
        return (
          <>
            <Spinner size="sm" />
            <span>Thinking</span>
          </>
        )
      case 'tool':
        return (
          <>
            <Spinner size="sm" />
            <span>
              Running {humanizeToolName(phase.tool)} <code className="font-mono text-xs">{phase.tool}</code>
            </span>
          </>
        )
      case 'streaming':
        return (
          <>
            <Spinner size="sm" />
            <span>Writing</span>
          </>
        )
      case 'waiting':
        return (
          <>
            <Hand className={cn('size-4', statusText.waiting)} aria-hidden />
            <span>{phase.what === 'approval' ? 'Waiting for your approval' : 'Waiting for your answer'}</span>
            {phase.onFocusRequest && (
              <Button variant="ghost" size="sm" onClick={phase.onFocusRequest}>
                Show request
              </Button>
            )}
          </>
        )
      case 'error':
        return (
          <>
            <CircleX className={cn('size-4', statusText.error)} aria-hidden />
            <span className="text-destructive">{phase.message.split('\n')[0]}</span>
            {phase.onRetry && (
              <Button variant="ghost" size="sm" onClick={phase.onRetry}>
                Retry
              </Button>
            )}
          </>
        )
      case 'done':
        return (
          <>
            <CircleCheck className={cn('size-4', statusText.success)} aria-hidden />
            <span>{doneCopy(phase.durationMs, phase.toolCount)}</span>
          </>
        )
    }
  })()

  return (
    <div role="status" aria-live="polite" className={cn('flex h-9 items-center gap-2 text-sm', className)}>
      {content}
    </div>
  )
}
