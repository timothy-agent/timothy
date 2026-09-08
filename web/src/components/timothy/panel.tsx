import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

interface PanelProps {
  title?: string
  description?: string
  // Control rendered left of the title, e.g. a sidebar toggle.
  leading?: ReactNode
  actions?: ReactNode
  density?: 'comfortable' | 'operational'
  headingLevel?: 'h2' | 'h3'
  children: ReactNode
  className?: string
  // Extra classes on the body wrapper, e.g. `flex flex-1 flex-col` so a
  // chart child can stretch to the panel's height.
  bodyClassName?: string
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
  leading,
  actions,
  density = 'comfortable',
  headingLevel = 'h2',
  children,
  className,
  bodyClassName,
}: PanelProps) {
  const hasHeader = title || description || actions || leading
  // A collapsed section passes false/null children: no body, no body padding.
  const hasBody = children !== null && children !== undefined && children !== false
  const Heading = headingLevel

  return (
    <div data-density={density} className={cn('rounded-md border border-border bg-card', className)}>
      {hasHeader && (
        <div
          className={cn(
            density === 'comfortable' ? (hasBody ? 'p-5 pb-0' : 'p-5') : 'border-b border-border px-4 py-3',
          )}
        >
          <div
            className={cn(
              'flex justify-between gap-4',
              description ? 'items-start' : 'items-center',
              density === 'comfortable' && hasBody && 'mb-4',
            )}
          >
            <div className="flex min-w-0 items-center gap-2">
              {leading}
              <div className="min-w-0">
                {title && <Heading className="text-sm leading-5 font-semibold">{title}</Heading>}
                {description && <p className="mt-0.5 text-xs leading-5 text-muted-foreground">{description}</p>}
              </div>
            </div>
            {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
          </div>
        </div>
      )}
      {hasBody && (
        <div className={cn(density === 'comfortable' ? cn('p-5', hasHeader && 'pt-0') : 'p-0', bodyClassName)}>
          {children}
        </div>
      )}
    </div>
  )
}
