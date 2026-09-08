import { Mail01Icon, GlobalIcon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { TelegramIcon } from '@/components/icons/TelegramIcon'
import type { Destination } from '../../api/types'

const destinationKindIcon = { email: Mail01Icon, webhook: GlobalIcon } as const

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
  return <HugeiconsIcon icon={destinationKindIcon[kind]} className={className} />
}
