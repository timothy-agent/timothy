import { useId, type ReactNode } from 'react'
import { Link } from 'react-router'

import { Card } from '../ui/card'

interface AddPresetTileProps {
  to: string
  title: string
  description: string
  tile: ReactNode
}

// AddPresetTile is one dashed preset tile in the "Add a provider"-style
// grid: a single Link, accessible name is the title alone, description
// reachable via aria-describedby.
export function AddPresetTile({ to, title, description, tile }: AddPresetTileProps) {
  const descId = useId()
  return (
    <Card asChild className="border-dashed p-0">
      <Link
        to={to}
        aria-label={title}
        aria-describedby={descId}
        className="flex items-center gap-3 rounded-md p-4 outline-none transition-colors duration-100 hover:border-foreground/20 hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
      >
        {tile}
        <span className="min-w-0">
          <span className="block text-sm font-semibold">{title}</span>
          <span id={descId} className="block truncate text-sm text-muted-foreground">
            {description}
          </span>
        </span>
      </Link>
    </Card>
  )
}
