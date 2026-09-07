import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'

import { cn } from '@/lib/utils'

interface EmptyStateProps {
  icon?: LucideIcon
  title: string
  description?: string
  action?: ReactNode
  density?: 'comfortable' | 'operational'
  className?: string
}

// Centered empty-state message. Not a card; the caller decides the
// container (section 12).
export function EmptyState({ icon: Icon, title, description, action, density = 'comfortable', className }: EmptyStateProps) {
  return (
    <div
      className={cn(
        'flex flex-col items-center text-center',
        density === 'comfortable' ? 'px-6 py-16' : 'px-4 py-8',
        className,
      )}
    >
      {Icon && (
        <div className="flex size-10 items-center justify-center rounded-md bg-muted text-muted-foreground">
          <Icon className="size-5" aria-hidden />
        </div>
      )}
      <p className="mt-4 text-sm font-semibold">{title}</p>
      {description && <p className="mt-1 max-w-sm text-sm text-muted-foreground">{description}</p>}
      {action && <div className="mt-6">{action}</div>}
    </div>
  )
}
