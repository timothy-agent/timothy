import { useId, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { Check, ChevronsUpDown, X } from 'lucide-react'

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

interface ComboboxSharedProps {
  options: ComboboxOption[]
  placeholder?: string
  searchPlaceholder?: string
  emptyText?: string
  disabled?: boolean
  size?: 'default' | 'sm'
  className?: string
  id?: string
  'aria-label'?: string
  'aria-labelledby'?: string
}

interface ComboboxSingleProps extends ComboboxSharedProps {
  multiple?: false
  value: string | undefined
  onChange: (value: string | undefined) => void
  renderOption?: (option: ComboboxOption, selected: boolean) => ReactNode
  renderValue?: (option: ComboboxOption) => ReactNode
  allowClear?: boolean
}

interface ComboboxMultipleProps extends ComboboxSharedProps {
  multiple: true
  value: string[]
  onChange: (value: string[]) => void
  renderOption?: (option: ComboboxOption, selected: boolean) => ReactNode
}

type ComboboxProps = ComboboxSingleProps | ComboboxMultipleProps

// cmdk's CommandItem owns the DOM node's aria-selected for its own
// keyboard-highlight tracking and rewrites it on every highlight change,
// clobbering any aria-selected passed in as a prop. This hook pins the
// attribute to the membership value regardless of cmdk's own writes.
function usePinnedAriaSelected(selected: boolean) {
  const ref = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    const node = ref.current
    if (!node) return
    node.setAttribute('aria-selected', String(selected))
  })
  useLayoutEffect(() => {
    const node = ref.current
    if (!node) return
    const observer = new MutationObserver(() => {
      if (node.getAttribute('aria-selected') !== String(selected)) {
        node.setAttribute('aria-selected', String(selected))
      }
    })
    observer.observe(node, { attributes: true, attributeFilter: ['aria-selected'] })
    return () => observer.disconnect()
  }, [selected])
  return ref
}

interface ComboboxRowProps {
  option: ComboboxOption
  selected: boolean
  pinAriaSelected: boolean
  renderOption?: (option: ComboboxOption, selected: boolean) => ReactNode
  onSelect: () => void
}

// One option row. pinAriaSelected is only set in multiple mode, where
// aria-selected must reflect membership rather than cmdk's own
// keyboard-highlight tracking (see usePinnedAriaSelected).
function ComboboxRow({ option, selected, pinAriaSelected, renderOption, onSelect }: ComboboxRowProps) {
  const pinnedRef = usePinnedAriaSelected(pinAriaSelected && selected)
  return (
    <CommandItem
      ref={pinAriaSelected ? pinnedRef : undefined}
      value={option.value}
      keywords={option.keywords ?? [option.label]}
      disabled={option.disabled}
      className="rounded-none px-3 py-2.5 items-start"
      onSelect={onSelect}
    >
      {renderOption ? (
        renderOption(option, selected)
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
      <Check className={cn('ml-auto mt-0.5 size-4', selected ? 'opacity-100' : 'opacity-0')} aria-hidden />
    </CommandItem>
  )
}

// Searchable single-select built on Popover + Command (cmdk). Trigger
// mirrors a select-style outline Button (section 14.12: model pickers
// use this at operational density in practice, the caller sets size).
//
// role="combobox" takes its accessible name only from aria-label or
// aria-labelledby, never from visible content, so a caller must supply
// one of the two (a Field label alone will not reach this trigger).
export function Combobox(props: ComboboxProps) {
  const {
    options,
    placeholder = 'Select...',
    searchPlaceholder = 'Search...',
    emptyText = 'No results',
    disabled,
    size = 'default',
    className,
    id,
    'aria-label': ariaLabel,
    'aria-labelledby': ariaLabelledBy,
  } = props
  const [open, setOpen] = useState(false)
  const reactId = useId()
  const triggerId = `combobox-trigger-${reactId}`
  const listId = `combobox-list-${reactId}`

  const isMultiple = props.multiple === true
  const selectedValues = isMultiple ? props.value : []
  const isSelected = (optionValue: string) =>
    isMultiple ? selectedValues.includes(optionValue) : optionValue === props.value
  const selectedSingle = isMultiple ? undefined : options.find((option) => option.value === props.value)
  const selectedMultiple = isMultiple ? options.filter((option) => selectedValues.includes(option.value)) : []

  function handleSelect(optionValue: string) {
    if (isMultiple) {
      const next = selectedValues.includes(optionValue)
        ? selectedValues.filter((v) => v !== optionValue)
        : [...selectedValues, optionValue]
      props.onChange(next)
      return
    }
    if (props.allowClear && optionValue === props.value) {
      props.onChange(undefined)
    } else {
      props.onChange(optionValue)
    }
    setOpen(false)
  }

  function removeChip(optionValue: string) {
    if (!isMultiple) return
    props.onChange(selectedValues.filter((v) => v !== optionValue))
  }

  const popover = (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          id={id ?? triggerId}
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
          <span className={cn('truncate', !selectedSingle && !isMultiple && 'text-muted-foreground')}>
            {isMultiple
              ? selectedMultiple.length > 0
                ? `${selectedMultiple.length} selected`
                : placeholder
              : selectedSingle
                ? props.renderValue
                  ? props.renderValue(selectedSingle)
                  : selectedSingle.label
                : placeholder}
          </span>
          <ChevronsUpDown className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        </Button>
      </PopoverTrigger>
      <PopoverContent id={listId} className="w-(--radix-popover-trigger-width) p-0">
        <Command>
          <CommandInput placeholder={searchPlaceholder} />
          <CommandList className="max-h-72 pt-1" aria-multiselectable={isMultiple || undefined}>
            <CommandEmpty>{emptyText}</CommandEmpty>
            <CommandGroup className="p-0 [&_[cmdk-group-items]]:divide-y [&_[cmdk-group-items]]:divide-border">
              {options.map((option) => (
                <ComboboxRow
                  key={option.value}
                  option={option}
                  selected={isSelected(option.value)}
                  pinAriaSelected={isMultiple}
                  renderOption={props.renderOption}
                  onSelect={() => handleSelect(option.value)}
                />
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )

  if (!isMultiple) {
    return popover
  }

  return (
    <div className="flex flex-col gap-2">
      {popover}
      {selectedMultiple.length > 0 && (
        <ul className="flex flex-wrap gap-1.5" aria-label={ariaLabel} aria-labelledby={ariaLabel ? undefined : ariaLabelledBy}>
          {selectedMultiple.map((option) => (
            <li key={option.value} className="flex items-center gap-1 rounded-md border border-input bg-muted px-2 py-1 text-xs">
              <span className="truncate">{option.label}</span>
              <button
                type="button"
                aria-label={`Remove ${option.label}`}
                onClick={() => removeChip(option.value)}
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="size-3" aria-hidden />
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
