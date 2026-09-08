import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { statusMeta, type Status } from './status'

// StatusBadge is the one badge for the status model (section 13.3):
// soft background, paired foreground, icon, and text together. Colour
// never carries status alone.
export function StatusBadge({
  status,
  label,
  size = 'default',
}: {
  status: Status
  label?: string
  size?: 'default' | 'sm'
}) {
  const meta = statusMeta[status]
  const Icon = meta.icon
  return (
    <Badge variant={meta.tone} size={size} data-status={status}>
      <Icon aria-hidden className={cn('size-3', meta.spin && 'animate-spin motion-keep')} />
      {label ?? meta.label}
    </Badge>
  )
}
