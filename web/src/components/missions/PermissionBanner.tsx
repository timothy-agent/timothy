import { Badge } from '../ui/badge'
import { Button } from '../ui/button'

function prettyArgs(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

// PermissionBanner renders only when a mission has a live
// pending_permission — the same once|session|deny vocabulary and tool
// detail chat's PermissionModal shows, just inline rather than modal
// since a mission isn't a focused single-turn interaction.
export function PermissionBanner({
  tool,
  args,
  danger,
  rationale,
  onDecide,
}: {
  tool?: string
  args?: string
  danger?: string
  rationale?: string
  onDecide: (decision: 'once' | 'session' | 'deny') => void
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
        </div>
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
      </div>
      {args && (
        <pre className="max-h-48 overflow-auto rounded bg-white/60 p-3 text-xs whitespace-pre-wrap text-amber-950 dark:bg-black/20 dark:text-amber-100">
          {prettyArgs(args)}
        </pre>
      )}
    </div>
  )
}
