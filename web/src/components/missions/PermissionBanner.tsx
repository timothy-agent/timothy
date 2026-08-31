import { Badge } from '../ui/badge'
import { Button } from '../ui/button'

function prettyArgs(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

// answeredCopy describes each decision's in-flight status line, shown
// once the broker has resolved but before the mission row's own
// pending_permission clears (that only happens once the approved tool
// call finishes executing, which for a long-running command can be
// minutes — the card must not look unanswered for that whole span).
const answeredCopy: Record<'once' | 'session' | 'deny' | 'unknown', string> = {
  once: 'Approved — command running…',
  session: 'Approved — command running…',
  deny: 'Denied — returning to worker…',
  unknown: 'Answered — waiting for the worker to continue…',
}

// PermissionBanner renders only when a mission has a live
// pending_permission — the same once|session|deny vocabulary and tool
// detail chat's PermissionModal shows, just inline rather than modal
// since a mission isn't a focused single-turn interaction. Once
// answered (answeredDecision set), the action buttons are replaced by
// a status line — the buttons are no longer valid after the broker
// resolves, and a second click would just 404.
export function PermissionBanner({
  tool,
  args,
  danger,
  rationale,
  answeredDecision,
  onDecide,
  timeoutSeconds,
}: {
  tool?: string
  args?: string
  danger?: string
  rationale?: string
  answeredDecision?: 'once' | 'session' | 'deny' | 'unknown'
  onDecide: (decision: 'once' | 'session' | 'deny') => void
  // timeoutSeconds is this mission's own permission_timeout_seconds
  // override, when set: an unattended mission auto-denies an
  // unanswered request after this many seconds. undefined means no
  // per-mission override (the global setting may still apply; that
  // value isn't exposed to the UI).
  timeoutSeconds?: number
}) {
  return (
    <div className="space-y-3 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 dark:border-amber-900 dark:bg-amber-950">
      <div className="flex items-start justify-between gap-3">
        <div className="space-y-1">
          <p className="flex items-center gap-2 text-sm font-medium text-amber-900 dark:text-amber-200">
            Allow <span className="font-mono">{tool || 'this tool call'}</span>?
            {danger === 'destructive' && <Badge variant="destructive">destructive</Badge>}
          </p>
          {rationale && <p className="text-sm text-amber-800 dark:text-amber-300">{rationale}</p>}
          {typeof timeoutSeconds === 'number' && timeoutSeconds > 0 && (
            <p className="text-xs text-amber-700 dark:text-amber-400">
              Auto-denies if unanswered for {timeoutSeconds}s
            </p>
          )}
        </div>
        {answeredDecision !== undefined ? (
          <p className="shrink-0 text-sm text-amber-800 dark:text-amber-300">
            {answeredCopy[answeredDecision]}
          </p>
        ) : (
          <div className="flex shrink-0 gap-2">
            <Button variant="ghost" size="sm" onClick={() => onDecide('deny')}>
              Deny
            </Button>
            <Button variant="outline" size="sm" onClick={() => onDecide('session')}>
              Allow for session
            </Button>
            <Button size="sm" onClick={() => onDecide('once')}>
              Allow once
            </Button>
          </div>
        )}
      </div>
      {args && (
        <pre className="max-h-48 overflow-auto rounded bg-white/60 p-3 text-xs whitespace-pre-wrap text-amber-950 dark:bg-black/20 dark:text-amber-100">
          {prettyArgs(args)}
        </pre>
      )}
    </div>
  )
}
