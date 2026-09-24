import { Globe, Mail, MessageSquare } from 'lucide-react'
import type { Destination } from '../../api/types'
import { isGitKind } from '../../lib/gitKinds'

const destinationKindIcon = { email: Mail, webhook: Globe, channel: MessageSquare } as const

// DestinationKindIcon renders the small glyph identifying a
// destination's kind — shared by the settings destinations list and
// the automations pages' destination badges.
export function DestinationKindIcon({
  kind,
  className = 'size-3.5',
}: {
  kind: Destination['kind']
  className?: string
}) {
  if (isGitKind(kind)) {
    return (
      <svg className={`${className} fill-current`} aria-hidden="true">
        <use href={`#clogo-${kind}`} />
      </svg>
    )
  }
  const Icon = destinationKindIcon[kind]
  return <Icon className={className} />
}
