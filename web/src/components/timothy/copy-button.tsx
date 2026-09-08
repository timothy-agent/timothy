import * as React from 'react'
import { Check, Copy } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

const COPIED_DURATION_MS = 1500

// CopyButton copies `value` to the clipboard and shows a check for
// 1.5s (section 8.8). Clipboard rejection is handled silently.
export function CopyButton({
  value,
  label = 'Copy',
  size = 'xs',
  className,
}: {
  value: string
  label?: string
  size?: 'default' | 'sm' | 'xs' | 'lg'
  className?: string
}) {
  const [copied, setCopied] = React.useState(false)

  const iconSize = {
    default: 'icon',
    sm: 'icon-sm',
    xs: 'icon-xs',
    lg: 'icon-lg',
  }[size] as 'icon' | 'icon-sm' | 'icon-xs' | 'icon-lg'

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      setTimeout(() => setCopied(false), COPIED_DURATION_MS)
    } catch {
      // Clipboard rejection: keep the copy icon, no error surfaced.
    }
  }

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button variant="ghost" size={iconSize} aria-label={label} onClick={handleCopy} className={className}>
          {copied ? <Check aria-hidden /> : <Copy aria-hidden />}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
      {copied && (
        <span role="status" className="sr-only">
          Copied
        </span>
      )}
    </Tooltip>
  )
}
