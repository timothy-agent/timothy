import { cn } from '@/lib/utils'
import { statusSolidBg, type Status } from './status'

// StatusDot is the dense-row status indicator (section 13.3): a solid
// pill next to an sr-only (or optionally visible) label. Never
// rendered without a text label somewhere.
export function StatusDot({
  status,
  label,
  size = 'default',
  showLabel = false,
}: {
  status: Status
  label: string
  size?: 'default' | 'sm'
  showLabel?: boolean
}) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span
        aria-hidden
        data-status={status}
        className={cn(
          'rounded-full',
          size === 'sm' ? 'size-1.5' : 'size-2',
          statusSolidBg[status],
          status === 'working' && 'animate-pulse',
        )}
      />
      {showLabel ? <span className="text-xs text-muted-foreground">{label}</span> : <span className="sr-only">{label}</span>}
    </span>
  )
}
