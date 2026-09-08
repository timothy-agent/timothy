import { Badge } from '../ui/badge'

// Memory type (semantic/episodic/procedural) is a category, not a status:
// one neutral badge, no per-type hue map (section 13).
export function TypeBadge({ type }: { type: string }) {
  return <Badge variant="neutral">{type}</Badge>
}
