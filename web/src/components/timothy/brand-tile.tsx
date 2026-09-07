import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

const sizeClass = {
  sm: 'size-7',
  md: 'size-9',
  lg: 'size-12',
} as const

interface BrandTileProps {
  spriteId?: string
  image?: string
  icon?: ReactNode
  color?: string
  size?: 'sm' | 'md' | 'lg'
  label?: string
  className?: string
}

// BrandTile is the one place a vendor colour is painted
// (style={{ backgroundColor }}, section 1.6). Square, one glyph scale.
// Precedence: spriteId, then image, then icon, then a dashed fallback.
export function BrandTile({ spriteId, image, icon, color, size = 'md', label, className }: BrandTileProps) {
  const a11y = label ? { role: 'img' as const, 'aria-label': label } : { 'aria-hidden': true as const }
  const base = cn('grid shrink-0 place-items-center rounded-md', sizeClass[size], className)

  if (spriteId) {
    return (
      <span className={cn(base, 'text-white')} style={{ backgroundColor: color }} {...a11y}>
        <svg className="size-[60%] fill-current">
          <use href={`#${spriteId}`} />
        </svg>
      </span>
    )
  }
  if (image) {
    return (
      <span className={cn(base, 'bg-muted/40')} {...a11y}>
        <img src={image} alt="" className="size-[70%] object-contain" />
      </span>
    )
  }
  if (icon) {
    return (
      <span
        className={cn(base, color ? 'text-white' : 'bg-muted text-muted-foreground')}
        style={color ? { backgroundColor: color } : undefined}
        {...a11y}
      >
        {icon}
      </span>
    )
  }
  return (
    <span className={cn(base, 'border-[1.5px] border-dashed border-border text-muted-foreground')} {...a11y}>
      ⌁
    </span>
  )
}
