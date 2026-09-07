import type { ReactNode } from 'react'

import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'

interface SegmentedOption {
  value: string
  label: string
  icon?: ReactNode
  disabled?: boolean
}

interface SegmentedControlProps {
  value: string
  onChange: (value: string) => void
  options: SegmentedOption[]
  size?: 'default' | 'sm'
  'aria-label': string
}

// Single-select toggle group that never allows an empty selection
// (section 8.5: segmented control uses the same brand-soft treatment
// as navigation for its selected segment).
export function SegmentedControl({ value, onChange, options, size = 'default', 'aria-label': ariaLabel }: SegmentedControlProps) {
  return (
    <ToggleGroup
      type="single"
      size={size}
      value={value}
      onValueChange={(next) => {
        if (next) onChange(next)
      }}
      aria-label={ariaLabel}
    >
      {options.map((option) => (
        <ToggleGroupItem key={option.value} value={option.value} disabled={option.disabled} aria-label={option.label}>
          {option.icon}
          {option.label}
        </ToggleGroupItem>
      ))}
    </ToggleGroup>
  )
}
