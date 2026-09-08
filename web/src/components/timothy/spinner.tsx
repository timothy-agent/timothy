import { Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'

const sizeClasses = {
  sm: 'size-3.5',
  default: 'size-4',
  lg: 'size-5',
} as const

// Spinner is the only spinner in the system (section 8.7).
export function Spinner({
  size = 'default',
  label = 'Loading',
  className,
}: {
  size?: 'sm' | 'default' | 'lg'
  label?: string
  className?: string
}) {
  return (
    <Loader2
      role="status"
      aria-label={label}
      className={cn('animate-spin motion-keep text-muted-foreground', sizeClasses[size], className)}
    />
  )
}
