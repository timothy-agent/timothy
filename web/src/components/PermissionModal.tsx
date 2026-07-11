import { useEffect } from 'react'
import type { PermissionRequestEvent } from '../api/types'
import { Badge } from './ui/badge'
import { Button } from './ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from './ui/dialog'

// PermissionModal shows one parked tool call and collects the
// decision. Keyboard: A = allow once, S = allow for session,
// D / Escape = deny. Closing the dialog denies — a dismissed prompt
// must never silently allow.
export function PermissionModal({
  request,
  onDecision,
}: {
  request: PermissionRequestEvent
  onDecision: (id: string, decision: 'once' | 'session' | 'deny') => void
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return
      const key = e.key.toLowerCase()
      if (key === 'a') onDecision(request.id, 'once')
      else if (key === 's') onDecision(request.id, 'session')
      else if (key === 'd') onDecision(request.id, 'deny')
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [request.id, onDecision])

  return (
    <Dialog open onOpenChange={(open) => !open && onDecision(request.id, 'deny')}>
      <DialogContent data-testid="permission-modal">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            Allow <span className="font-mono">{request.tool}</span>?
            {request.danger_level === 'destructive' && (
              <Badge variant="destructive" data-testid="danger-badge">
                destructive
              </Badge>
            )}
          </DialogTitle>
          <DialogDescription>{request.rationale}</DialogDescription>
        </DialogHeader>
        <pre className="max-h-48 overflow-auto rounded bg-zinc-100 p-3 text-xs whitespace-pre-wrap text-zinc-700 dark:bg-zinc-800 dark:text-zinc-200">
          {prettyArgs(request.args)}
        </pre>
        <DialogFooter className="gap-2">
          <Button variant="ghost" onClick={() => onDecision(request.id, 'deny')}>
            Deny <kbd className="ml-1 text-xs opacity-60">D</kbd>
          </Button>
          <Button variant="outline" onClick={() => onDecision(request.id, 'session')}>
            Allow for session <kbd className="ml-1 text-xs opacity-60">S</kbd>
          </Button>
          <Button onClick={() => onDecision(request.id, 'once')}>
            Allow once <kbd className="ml-1 text-xs opacity-60">A</kbd>
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function prettyArgs(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}
