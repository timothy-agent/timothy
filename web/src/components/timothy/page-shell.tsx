import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

const widthClasses = {
  reading: 'max-w-[45rem]',
  form: 'max-w-2xl',
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
        'mx-auto w-full px-4 py-4 sm:px-6 sm:py-6 lg:px-8 lg:py-8',
        widthClasses[width],
        className,
      )}
    >
      {children}
    </div>
  )
}
