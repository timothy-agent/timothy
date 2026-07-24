import { Badge } from '../ui/badge'

const typeTone: Record<string, string> = {
  semantic: 'bg-blue-500/15 text-blue-600 dark:text-blue-400',
  episodic: 'bg-amber-500/15 text-amber-600 dark:text-amber-400',
  procedural: 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400',
}

export function TypeBadge({ type }: { type: string }) {
  return (
    <Badge variant="outline" className={`border-0 ${typeTone[type] ?? ''}`}>
      {type}
    </Badge>
  )
}
