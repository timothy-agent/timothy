import { Globe, MessageSquare } from 'lucide-react'
import gmailLogo from '../../assets/connectors/gmail.png'
import type { Destination } from '../../api/types'
import { isGitKind } from '../../lib/gitKinds'
import { BrandTile } from '../timothy/brand-tile'

const gitBrandColor = {
  github: '#24292F',
  bitbucket: '#0052CC',
  gitlab: '#FC6D26',
} as const

// DestinationKindIcon renders the brand tile identifying a destination's
// kind — shared by the settings destinations list and the automations
// pages' destination badges. Matches Providers/Connectors: brand color
// + glyph, or a full-color PNG for Gmail-backed email.
export function DestinationKindIcon({
  kind,
  className = 'size-9',
}: {
  kind: Destination['kind']
  className?: string
}) {
  if (isGitKind(kind)) {
    return <BrandTile spriteId={`clogo-${kind}`} color={gitBrandColor[kind]} className={className} />
  }
  if (kind === 'email') {
    return <BrandTile image={gmailLogo} className={className} />
  }
  if (kind === 'webhook') {
    return (
      <BrandTile
        icon={<Globe className="size-[60%]" />}
        color="#0EA5E9"
        className={className}
      />
    )
  }
  return (
    <BrandTile
      icon={<MessageSquare className="size-[60%]" />}
      color="#6366F1"
      className={className}
    />
  )
}
