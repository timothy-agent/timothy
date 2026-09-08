import type { ComponentProps, ReactNode } from 'react'
import { ChevronRight } from 'lucide-react'
import { Link } from 'react-router'

import { cn } from '@/lib/utils'

interface BreadcrumbItem {
  label: string
  href?: string
}

// Breadcrumb trail. The last item is the current page: plain text,
// aria-current="page", not a link (section 11.2).
export function Breadcrumbs({ items }: { items: BreadcrumbItem[] }) {
  return (
    <nav aria-label="Breadcrumb">
      <ol className="flex items-center gap-1.5 text-xs text-muted-foreground">
        {items.map((item, i) => {
          const isLast = i === items.length - 1
          return (
            <li key={`${item.label}-${i}`} className="flex items-center gap-1.5">
              {i > 0 && <ChevronRight aria-hidden className="size-3" />}
              {isLast || !item.href ? (
                <span aria-current={isLast ? 'page' : undefined} className={isLast ? 'text-foreground' : undefined}>
                  {item.label}
                </span>
              ) : (
                <Link to={item.href} className="hover:text-foreground">
                  {item.label}
                </Link>
              )}
            </li>
          )
        })}
      </ol>
    </nav>
  )
}

interface PageHeaderProps {
  title: string
  titleNode?: ReactNode
  description?: string
  breadcrumbs?: BreadcrumbItem[]
  actions?: ReactNode
  meta?: ReactNode
  children?: ReactNode
}

// Page-level heading: breadcrumb, title row (title + meta on the left,
// actions on the right), optional children slot (e.g. a phase
// stepper) between the title row and the description (section 11.3).
// titleNode replaces the rendered h1 when the title needs a sibling
// element inside the heading (a status badge, a mono identifier); it
// is not for an editable title (11.3).
export function PageHeader({ title, titleNode, description, breadcrumbs, actions, meta, children }: PageHeaderProps) {
  return (
    <div className="mb-8">
      {breadcrumbs && breadcrumbs.length > 0 && (
        <div className="mb-2">
          <Breadcrumbs items={breadcrumbs} />
        </div>
      )}
      <div className="flex flex-col items-start justify-between gap-4 sm:flex-row">
        <div className="flex items-center gap-3">
          {titleNode ?? <h1 className="text-title font-semibold text-foreground">{title}</h1>}
          {meta}
        </div>
        {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
      </div>
      {children && <div className="mt-3">{children}</div>}
      {description && <p className="mt-1 text-xs leading-5 text-muted-foreground">{description}</p>}
    </div>
  )
}

interface SectionHeaderProps {
  title: string
  description?: string
  actions?: ReactNode
  as?: 'h2'
}

// Section-level heading used to group panels or cards on a page.
export function SectionHeader({ title, description, actions, as: As = 'h2' }: SectionHeaderProps) {
  return (
    <div className="mb-4">
      <div className="flex items-start justify-between gap-4">
        <As className="text-section font-semibold text-foreground">{title}</As>
        {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
      </div>
      {description && <p className="mt-0.5 text-xs leading-5 text-muted-foreground">{description}</p>}
    </div>
  )
}

// Group label for sidebars, lists and operational panels (section 3.2).
export function Eyebrow({ className, ...props }: ComponentProps<'span'>) {
  return (
    <span
      className={cn('text-xs font-medium tracking-[0.04em] text-muted-foreground uppercase', className)}
      {...props}
    />
  )
}
