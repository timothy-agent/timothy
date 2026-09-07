import { StatusBadge } from '@/components/timothy/status-badge'
import { Progress } from '@/components/ui/progress'
import { cn } from '@/lib/utils'
import { money } from '@/lib/format'
import type { ReactNode } from 'react'

// CostDisplay is the one cost renderer (contract 14.9): text plus a
// budget-aware bar that turns warning at 80% and error at 100%.
export function CostDisplay({
  cost,
  currency,
  budget,
  unpricedRequests,
  detail,
  className,
}: {
  cost: number
  currency: string
  budget?: number
  unpricedRequests?: number
  detail?: ReactNode
  className?: string
}) {
  const percent = budget ? (cost / budget) * 100 : undefined
  const tone = percent === undefined ? 'brand' : percent >= 100 ? 'destructive' : percent >= 80 ? 'warning' : 'brand'

  return (
    <div className={cn('space-y-1.5', className)}>
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm tabular-nums">
          {budget !== undefined ? `${money(cost, currency)} of ${money(budget, currency)}` : money(cost, currency)}
        </span>
        {percent !== undefined && <span className="text-xs tabular-nums text-muted-foreground">{Math.round(percent)}%</span>}
      </div>
      {budget !== undefined && (
        <Progress value={Math.min(percent ?? 0, 100)} tone={tone} aria-label="Budget used" />
      )}
      {percent !== undefined && percent >= 100 && <StatusBadge status="error" label="Over budget" size="sm" />}
      {percent !== undefined && percent >= 80 && percent < 100 && <StatusBadge status="warning" label="Near budget" size="sm" />}
      {unpricedRequests !== undefined && unpricedRequests > 0 && (
        <p className="text-xs text-muted-foreground">
          {unpricedRequests} unpriced call{unpricedRequests === 1 ? '' : 's'}
        </p>
      )}
      {detail && <p className="text-xs text-muted-foreground">{detail}</p>}
    </div>
  )
}
