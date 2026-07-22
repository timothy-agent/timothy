import { useMemo, useRef, useState } from 'react'
import { Input } from '../ui/input'
import { Popover, PopoverContent, PopoverAnchor } from '../ui/popover'

export interface ModelSuggestion {
  id: string
  hint?: string
}

// ModelInput is a free-text field with advisory autocomplete: it never
// blocks an unlisted id (models change too often to gate on a fixed
// catalog), it just surfaces ones we already have real evidence for —
// a preset's validated default, or ids declared on other configured
// providers of the same driver.
export function ModelInput({
  value,
  onChange,
  suggestions,
  placeholder,
  className,
  id,
}: {
  value: string
  onChange: (v: string) => void
  suggestions: ModelSuggestion[]
  placeholder?: string
  className?: string
  id?: string
}) {
  const [open, setOpen] = useState(false)
  const skipNextOpen = useRef(false)
  const inputRef = useRef<HTMLInputElement>(null)

  const filtered = useMemo(() => {
    const q = value.trim().toLowerCase()
    const list = q ? suggestions.filter((s) => s.id.toLowerCase().includes(q)) : suggestions
    return list.slice(0, 8)
  }, [suggestions, value])

  return (
    <Popover open={open && filtered.length > 0} onOpenChange={setOpen}>
      <PopoverAnchor asChild>
        <Input
          ref={inputRef}
          id={id}
          value={value}
          onChange={(e) => {
            onChange(e.target.value)
            setOpen(true)
          }}
          onFocus={() => {
            if (!skipNextOpen.current) setOpen(true)
            skipNextOpen.current = false
          }}
          onKeyDown={(e) => {
            if (e.key === 'Escape') setOpen(false)
          }}
          placeholder={placeholder}
          className={className}
          autoComplete="off"
        />
      </PopoverAnchor>
      <PopoverContent
        align="start"
        className="w-(--radix-popover-trigger-width) p-1"
        onOpenAutoFocus={(e) => e.preventDefault()}
        // The anchor is a plain input focused by the same click that
        // opens this popover — Radix's outside-click detection can
        // otherwise see that first click as "outside" (content isn't
        // mounted yet when the pointerdown fires) and close it on the
        // same interaction that opened it.
        onInteractOutside={(e) => {
          if (e.target === inputRef.current) e.preventDefault()
        }}
      >
        <ul role="listbox" className="max-h-56 overflow-y-auto">
          {filtered.map((s) => (
            <li key={s.id}>
              <button
                type="button"
                role="option"
                aria-selected={s.id === value}
                onClick={() => {
                  onChange(s.id)
                  skipNextOpen.current = true
                  setOpen(false)
                }}
                className="flex w-full items-center justify-between gap-3 rounded-md px-2.5 py-1.5 text-left text-sm hover:bg-muted"
              >
                <span className="truncate font-mono">{s.id}</span>
                {s.hint && <span className="shrink-0 text-xs text-muted-foreground">{s.hint}</span>}
              </button>
            </li>
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  )
}
