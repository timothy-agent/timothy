import type { ReactNode } from 'react'

import { Panel } from './panel'

interface StatTileProps {
  label: string
  value: ReactNode
  hint?: string
  children?: ReactNode
}

// One headline number in a Panel, with an optional hint line and a
// slot below for a small chart.
export function StatTile({ label, value, hint, children }: StatTileProps) {
  return (
    <Panel>
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="mt-1.5 text-2xl font-semibold tracking-tight tabular-nums">{value}</p>
      {hint && <p className="mt-0.5 text-xs text-muted-foreground">{hint}</p>}
      {children && <div className="mt-2">{children}</div>}
    </Panel>
  )
}
