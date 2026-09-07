import * as React from 'react'
import { cn } from '@/lib/utils'

// Kbd renders a single key hint chip (section 14.7 shortcut hints).
export function Kbd({ className, ...props }: React.ComponentProps<'kbd'>) {
  return (
    <kbd
      data-slot="kbd"
      className={cn(
        'inline-flex h-5 min-w-5 items-center justify-center rounded-md border border-border bg-muted px-1 font-sans text-xs text-muted-foreground',
        className,
      )}
      {...props}
    />
  )
}

// KbdGroup wraps a sequence of Kbd chips, e.g. a chord.
export function KbdGroup({ className, ...props }: React.ComponentProps<'span'>) {
  return <span className={cn('inline-flex items-center gap-1', className)} {...props} />
}
