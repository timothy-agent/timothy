import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

// Every page kind fills the content area; the width prop is kept as a
// data attribute so page kind stays queryable and a measure can return
// per kind without touching call sites.
const widthClasses = {
  reading: '',
  form: '',
  full: '',
} as const

interface PageShellProps {
  width?: keyof typeof widthClasses
  children: ReactNode
  className?: string
}

// Sets page margins and content max width by page kind (section 7.4).
export function PageShell({ width = 'full', children, className }: PageShellProps) {
  return (
    <div
      data-width={width}
      className={cn(
        // The app shell clips its content area (Chat owns its own
        // scroller), so a page taller than the viewport scrolls here.
        'mx-auto h-full w-full overflow-y-auto px-4 py-4 sm:px-6 sm:py-6 lg:px-8 lg:py-8',
        widthClasses[width],
        className,
      )}
    >
      {children}
    </div>
  )
}
