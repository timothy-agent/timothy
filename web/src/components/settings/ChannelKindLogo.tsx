import gmailLogo from '../../assets/connectors/gmail.png'
import type { Channel } from '../../api/types'
import { TelegramIcon } from '../icons/TelegramIcon'
import { BrandTile } from '../timothy/brand-tile'

// ChannelKindLogo paints the official brand tile for a channel kind —
// same treatment Providers/Connectors use (color + glyph, or PNG).
export function ChannelKindLogo({
  kind,
  className = 'size-9',
}: {
  kind: Channel['kind']
  className?: string
}) {
  if (kind === 'slack') {
    return <BrandTile spriteId="clogo-slack" color="#4A154B" className={className} />
  }
  if (kind === 'email') {
    return <BrandTile image={gmailLogo} className={className} />
  }
  return (
    <BrandTile
      icon={<TelegramIcon className="size-[60%]" />}
      color="#26A5E4"
      className={className}
    />
  )
}
