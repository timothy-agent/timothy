import { useId, useState, type ReactNode } from 'react'
import { Check, ChevronsUpDown } from 'lucide-react'

import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from '@/components/ui/command'

export interface ComboboxOption {
  value: string
  label: string
  description?: string
  icon?: ReactNode
  disabled?: boolean
  keywords?: string[]
}

interface ComboboxProps {
  options: ComboboxOption[]
  value: string | undefined
  onChange: (value: string | undefined) => void
  placeholder?: string
  searchPlaceholder?: string
  emptyText?: string
  disabled?: boolean
  size?: 'default' | 'sm'
  renderOption?: (option: ComboboxOption, selected: boolean) => ReactNode
  renderValue?: (option: ComboboxOption) => ReactNode
  allowClear?: boolean
  className?: string
  'aria-label'?: string
  'aria-labelledby'?: string
}

// Searchable single-select built on Popover + Command (cmdk). Trigger
// mirrors a select-style outline Button (section 14.12: model pickers
// use this at operational density in practice, the caller sets size).
//
// role="combobox" takes its accessible name only from aria-label or
// aria-labelledby, never from visible content, so a caller must supply
// one of the two (a Field label alone will not reach this trigger).
export function Combobox({
  options,
  value,
  onChange,
  placeholder = 'Select...',
  searchPlaceholder = 'Search...',
  emptyText = 'No results',
  disabled,
  size = 'default',
  renderOption,
  renderValue,
  allowClear,
  className,
  'aria-label': ariaLabel,
  'aria-labelledby': ariaLabelledBy,
}: ComboboxProps) {
  const [open, setOpen] = useState(false)
  const selected = options.find((option) => option.value === value)
  const reactId = useId()
  const triggerId = `combobox-trigger-${reactId}`
  const listId = `combobox-list-${reactId}`

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          id={triggerId}
          variant="outline"
          size={size}
          role="combobox"
          aria-expanded={open}
          aria-controls={listId}
          aria-label={ariaLabel}
          aria-labelledby={ariaLabelledBy}
          disabled={disabled}
          className={cn('w-full justify-between font-normal', className)}
        >
          <span className={cn('truncate', !selected && 'text-muted-foreground')}>
            {selected ? (renderValue ? renderValue(selected) : selected.label) : placeholder}
          </span>
          <ChevronsUpDown className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        </Button>
      </PopoverTrigger>
      <PopoverContent id={listId} className="w-(--radix-popover-trigger-width) p-0">
        <Command>
          <CommandInput placeholder={searchPlaceholder} />
          <CommandList className="max-h-72 pt-1">
            <CommandEmpty>{emptyText}</CommandEmpty>
            <CommandGroup className="p-0 [&_[cmdk-group-items]]:divide-y [&_[cmdk-group-items]]:divide-border">
              {options.map((option) => {
                const isSelected = option.value === value
                return (
                  <CommandItem
                    key={option.value}
                    value={option.value}
                    keywords={option.keywords ?? [option.label]}
                    disabled={option.disabled}
                    className="rounded-none px-3 py-2.5 items-start"
                    onSelect={() => {
                      if (allowClear && isSelected) {
                        onChange(undefined)
                      } else {
                        onChange(option.value)
                      }
                      setOpen(false)
                    }}
                  >
                    {renderOption ? (
                      renderOption(option, isSelected)
                    ) : (
                      <>
                        {option.icon}
                        <div className="flex min-w-0 flex-col gap-0.5">
                          <span className="truncate text-sm">{option.label}</span>
                          {option.description && (
                            <span className="truncate text-xs text-muted-foreground">{option.description}</span>
                          )}
                        </div>
                      </>
                    )}
                    <Check className={cn('ml-auto mt-0.5 size-4', isSelected ? 'opacity-100' : 'opacity-0')} aria-hidden />
                  </CommandItem>
                )
              })}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
