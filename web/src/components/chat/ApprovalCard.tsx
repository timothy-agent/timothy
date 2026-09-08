import { ApprovalCardShell, useGateShortcuts } from '@/components/timothy/approval-card'
import { JsonBlock } from '@/components/timothy/json-block'
import { Kbd } from '@/components/timothy/kbd'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import type { PermissionRequestEvent } from '@/api/types'
import { toolCategory, consequenceLine } from '@/lib/chatUi'

export type Decision = 'once' | 'session' | 'deny'

function parseArgs(raw: string): unknown {
  try {
    return JSON.parse(raw)
  } catch {
    return raw
  }
}

function formatTime(d: Date): string {
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false })
}

// ApprovalCard is the one component for every human permission gate
// (14.7): comfortable, prominent, a 3px warning left rule.
export function ApprovalCard({
  request,
  onDecision,
  requestedAt,
  className,
  autoFocus,
  id,
}: {
  request: PermissionRequestEvent
  onDecision: (id: string, d: Decision) => void
  requestedAt?: Date
  className?: string
  autoFocus?: boolean
  id?: string
}) {
  const category = toolCategory(request.tool)
  const title =
    category === 'shell' ? (
      <>
        Timothy wants to run a command <code className="font-mono">{request.tool}</code>
      </>
    ) : (
      <>
        Timothy wants to use <code className="font-mono">{request.tool}</code>
      </>
    )

  const handleKeyDown = useGateShortcuts({
    a: () => onDecision(request.id, 'once'),
    s: () => onDecision(request.id, 'session'),
    d: () => onDecision(request.id, 'deny'),
  })

  return (
    <ApprovalCardShell
      id={id}
      className={className}
      autoFocus={autoFocus}
      onKeyDown={handleKeyDown}
      title={title}
      badges={
        request.danger_level === 'destructive' && (
          <Badge variant="destructive" data-testid="danger-badge">
            destructive
          </Badge>
        )
      }
      meta={requestedAt && formatTime(requestedAt)}
      rationale={request.rationale.length > 0 ? request.rationale : undefined}
      consequence={consequenceLine(request)}
      actions={
        <>
          <Button variant="outline" onClick={() => onDecision(request.id, 'deny')}>
            Deny
            <Kbd className="ml-1.5">D</Kbd>
          </Button>
          <Button variant="outline" onClick={() => onDecision(request.id, 'session')}>
            Allow for session
            <Kbd className="ml-1.5">S</Kbd>
          </Button>
          <Button onClick={() => onDecision(request.id, 'once')}>
            Allow once
            <Kbd className="ml-1.5">A</Kbd>
          </Button>
        </>
      }
    >
      <div>
        <JsonBlock value={parseArgs(request.args)} density="trace" label="Arguments" maxLines={12} />
      </div>
    </ApprovalCardShell>
  )
}

// ApprovalDialog is the modal presentation of the same card, used
// only when the request blocks the turn and the transcript has
// scrolled away from it (14.7). Closing never decides.
export function ApprovalDialog({
  request,
  onDecision,
  open,
  onOpenChange,
}: {
  request: PermissionRequestEvent
  onDecision: (id: string, d: Decision) => void
  open: boolean
  onOpenChange: (o: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl border-0 p-0" aria-describedby={undefined}>
        <DialogTitle className="sr-only">Approval requested</DialogTitle>
        <ApprovalCard request={request} onDecision={onDecision} autoFocus />
      </DialogContent>
    </Dialog>
  )
}
