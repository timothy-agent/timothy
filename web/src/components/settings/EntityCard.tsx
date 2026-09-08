import type { ReactNode } from 'react'
import { Link } from 'react-router'

import { Card } from '../ui/card'

interface EntityCardProps {
  to: string
  title: string
  tile: ReactNode
  badges?: ReactNode
  summary?: ReactNode
  status?: ReactNode
  footer?: ReactNode
}

// EntityCard is the managed-resource card (contract 12): a Card with
// two interaction regions. Region 1 is a Link on the title plus
// content; region 2 is a footer below a divider, outside the Link's
// subtree. Footer controls (Switch, Test, Manage, Delete) never
// navigate; the Link never contains another interactive element.
export function EntityCard({ to, title, tile, badges, summary, status, footer }: EntityCardProps) {
  return (
    <Card className="flex flex-col gap-3">
      <Link to={to} className="group flex min-w-0 items-start gap-3 rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background">
        {tile}
        <div className="min-w-0 flex-1 space-y-2">
          <div className="flex items-center gap-2">
            <span className="truncate text-sm font-semibold group-hover:underline">{title}</span>
            {badges}
          </div>
          {summary}
          {status}
        </div>
      </Link>
      {footer && <div className="-mx-5 -mb-5 mt-auto flex items-center gap-2 border-t border-border px-5 py-3">{footer}</div>}
    </Card>
  )
}
