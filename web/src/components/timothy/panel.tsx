import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

interface PanelProps {
  title?: string
  description?: string
  actions?: ReactNode
  density?: 'comfortable' | 'operational'
  headingLevel?: 'h2' | 'h3'
  children: ReactNode
  className?: string
}

// Bounded region of related content on a page: settings group, mission
// goal, event log frame (section 12). Comfortable panels pad the body
// and the header; operational panels drop body padding so rows run
// edge to edge and the children own their own dividers.
//
// Panels must not nest: a sub-group inside a panel is a Section or a
// muted inset block, never another Panel.
//
// headingLevel controls the title's heading tag, not its style (always
// text-sm leading-5 font-semibold): a Panel is normally a top-level
// page section (h2, the default); use h3 only when the Panel sits
// under a real SectionHeader/h2.
export function Panel({
  title,
  description,
  actions,
  density = 'comfortable',
  headingLevel = 'h2',
  children,
  className,
}: PanelProps) {
  const hasHeader = title || description || actions
  const Heading = headingLevel

  return (
    <div data-density={density} className={cn('rounded-md border border-border bg-card', className)}>
      {hasHeader && (
        <div
          className={cn(
            density === 'comfortable' ? 'p-5 pb-0' : 'border-b border-border px-4 py-3',
          )}
        >
          <div className={cn('flex items-start justify-between gap-4', density === 'comfortable' && 'mb-4')}>
            <div>
              {title && <Heading className="text-sm leading-5 font-semibold">{title}</Heading>}
              {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
            </div>
            {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
          </div>
        </div>
      )}
      <div className={density === 'comfortable' ? cn('p-5', hasHeader && 'pt-0') : 'p-0'}>{children}</div>
    </div>
  )
}
