import { Globe, Mail } from 'lucide-react'
import { TelegramIcon } from '@/components/icons/TelegramIcon'
import type { Destination } from '../../api/types'

const destinationKindIcon = { email: Mail, webhook: Globe } as const

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
  if (kind === 'telegram') return <TelegramIcon className={className} />
  if (kind === 'github') {
    return (
      <svg className={`${className} fill-current`} aria-hidden="true">
        <use href="#clogo-github" />
      </svg>
    )
  }
  const Icon = destinationKindIcon[kind]
  return <Icon className={className} />
}
