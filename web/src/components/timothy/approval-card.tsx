import { useEffect, useId, useRef, type ReactNode } from 'react'
import { Hand } from 'lucide-react'

import { StatusBadge } from './status-badge'
import { cn } from '@/lib/utils'

// useGateShortcuts wires a keydown handler for the single-letter
// shortcuts every human gate uses (14.7): ignores INPUT/TEXTAREA
// targets and any modifier key, so typing in a steering-note textarea
// never fires a decision.
export function useGateShortcuts(map: Record<string, () => void>) {
  return (e: React.KeyboardEvent<HTMLElement>) => {
    const target = e.target as HTMLElement
    if (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA') return
    if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return
    const key = e.key.toLowerCase()
    const fn = map[key]
    if (fn) {
      e.preventDefault()
      fn()
    }
  }
}

// ApprovalCardShell is the presentational shell shared by every human
// gate (14.7): chat permission, mission permission, plan approval,
// input request. Callers supply the what/why/consequence/actions.
export function ApprovalCardShell({
  title,
  badges,
  meta,
  children,
  rationale,
  consequence,
  actions,
  answered,
  onKeyDown,
  autoFocus,
  id,
  className,
}: {
  title: ReactNode
  badges?: ReactNode
  meta?: ReactNode
  children?: ReactNode
  rationale?: string
  consequence?: ReactNode
  actions?: ReactNode
  answered?: ReactNode
  onKeyDown?: (e: React.KeyboardEvent<HTMLElement>) => void
  autoFocus?: boolean
  id?: string
  className?: string
}) {
  const titleId = useId()
  const sectionRef = useRef<HTMLElement>(null)

  useEffect(() => {
    if (autoFocus) sectionRef.current?.focus()
  }, [autoFocus])

  return (
    <section
      id={id}
      ref={sectionRef}
      role="region"
      aria-labelledby={titleId}
      tabIndex={-1}
      onKeyDown={onKeyDown}
      className={cn('min-w-0 space-y-4 rounded-md border border-l-[3px] border-border border-l-warning bg-card p-5', className)}
    >
      <div className="flex items-center gap-2">
        <Hand className="size-4 text-warning" aria-hidden />
        <h2 id={titleId} className="text-sm font-semibold">
          {title}
        </h2>
        <StatusBadge status="waiting" label="Waiting" size="sm" />
        {badges}
        {meta && <span className="ml-auto text-xs text-muted-foreground">{meta}</span>}
      </div>
      {children}
      {rationale && (
        <blockquote className="min-w-0 break-words border-l-2 border-border pl-3 text-sm text-foreground [overflow-wrap:anywhere]">
          {rationale}
        </blockquote>
      )}
      {consequence && <p className="min-w-0 break-words text-sm text-muted-foreground [overflow-wrap:anywhere]">{consequence}</p>}
      {answered ? answered : actions && <div className="flex flex-wrap items-center justify-end gap-2">{actions}</div>}
    </section>
  )
}
