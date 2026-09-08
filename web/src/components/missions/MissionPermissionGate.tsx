import { ApprovalCardShell, useGateShortcuts } from '../timothy/approval-card'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { JsonBlock } from '../timothy/json-block'
import { Kbd } from '../timothy/kbd'
import { consequenceLine } from '../../lib/chatUi'

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

function parseArgs(raw?: string): unknown {
  if (!raw) return undefined
  try {
    return JSON.parse(raw)
  } catch {
    return raw
  }
}

// MissionPermissionGate renders only when a mission has a live
// pending_permission — the same once|session|deny vocabulary and tool
// detail chat's PermissionModal shows, on the shared ApprovalCardShell
// (contract 14.7). Once answered (answeredDecision set), the action
// buttons are replaced by a status line — the buttons are no longer
// valid after the broker resolves, and a second click would just 404.
export function MissionPermissionGate({
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
  const handleKeyDown = useGateShortcuts({
    d: () => onDecide('deny'),
    s: () => onDecide('session'),
    a: () => onDecide('once'),
  })

  return (
    <ApprovalCardShell
      title={
        tool ? (
          <>
            Timothy wants to run <code>{tool}</code>
          </>
        ) : (
          'Timothy wants to use this tool'
        )
      }
      badges={danger === 'destructive' && <Badge variant="destructive">destructive</Badge>}
      meta={
        typeof timeoutSeconds === 'number' && timeoutSeconds > 0
          ? `Auto-denies if unanswered for ${timeoutSeconds}s`
          : undefined
      }
      rationale={rationale}
      consequence={consequenceLine({
        id: '',
        call_id: '',
        tool: tool ?? '',
        args: args ?? '',
        danger_level: danger === 'destructive' ? 'destructive' : 'safe',
        rationale: rationale ?? '',
      })}
      onKeyDown={answeredDecision === undefined ? handleKeyDown : undefined}
      autoFocus={answeredDecision === undefined}
      answered={
        answeredDecision !== undefined && <p className="text-sm text-muted-foreground">{answeredCopy[answeredDecision]}</p>
      }
      actions={
        <>
          <Button variant="ghost" onClick={() => onDecide('deny')}>
            Deny <Kbd aria-hidden>D</Kbd>
          </Button>
          <Button variant="outline" onClick={() => onDecide('session')}>
            Allow for session <Kbd aria-hidden>S</Kbd>
          </Button>
          <Button onClick={() => onDecide('once')}>
            Allow once <Kbd aria-hidden>A</Kbd>
          </Button>
        </>
      }
    >
      {args && <JsonBlock value={parseArgs(args)} density="trace" label="Arguments" maxLines={12} />}
    </ApprovalCardShell>
  )
}
