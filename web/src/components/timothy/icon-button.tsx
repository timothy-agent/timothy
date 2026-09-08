import * as React from 'react'
import type { LucideIcon } from 'lucide-react'
import { Button, buttonVariants } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { Spinner } from './spinner'
import type { VariantProps } from 'class-variance-authority'

const iconSizeByButtonSize = {
  default: 'icon',
  sm: 'icon-sm',
  xs: 'icon-xs',
  lg: 'icon-lg',
} as const

const spinnerSizeByButtonSize = {
  default: 'sm',
  sm: 'sm',
  xs: 'sm',
  lg: 'default',
} as const

// IconButton wraps Button for an icon-only control: always has an
// accessible label, and shows it as a tooltip by default.
export const IconButton = React.forwardRef<
  HTMLButtonElement,
  {
    label: string
    icon: LucideIcon
    size?: 'default' | 'sm' | 'xs' | 'lg'
    variant?: VariantProps<typeof buttonVariants>['variant']
    tooltip?: boolean
    loading?: boolean
  } & Omit<React.ComponentProps<typeof Button>, 'size' | 'variant' | 'children'>
>(function IconButton(
  { label, icon: Icon, size = 'default', variant = 'ghost', tooltip = true, loading = false, disabled, ...props },
  ref,
) {
  const button = (
    <Button
      ref={ref}
      variant={variant}
      size={iconSizeByButtonSize[size]}
      aria-label={label}
      aria-busy={loading || undefined}
      disabled={disabled || loading}
      {...props}
    >
      {loading ? <Spinner size={spinnerSizeByButtonSize[size]} label={label} /> : <Icon aria-hidden />}
    </Button>
  )

  if (!tooltip) return button

  return (
    <Tooltip>
      <TooltipTrigger asChild>{button}</TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
})
