import { useEffect, useState } from 'react'
import { ChevronDownIcon } from 'lucide-react'
import { listKbCollections } from '../../api/client'
import type { KbCollection } from '../../api/types'
import { Button } from '../ui/button'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '../ui/command'
import { Popover, PopoverContent, PopoverTrigger } from '../ui/popover'

// Module-level cache: the picker can render in every agent form; the
// collection surface changes rarely and a stale list self-heals on
// the next mount.
let cachedCollections: KbCollection[] | null = null

function useKbCollections(): KbCollection[] {
  const [collections, setCollections] = useState<KbCollection[]>(cachedCollections ?? [])
  useEffect(() => {
    listKbCollections().then(
      (list) => {
        cachedCollections = list
        setCollections(list)
      },
      () => undefined,
    )
  }, [])
  return collections
}

// KnowledgePicker lets an agent's knowledge allowlist be built from
// the actual collections instead of typed blind — the exact
// collection name is otherwise undiscoverable. Falls back to
// comma-separated free text when the list is empty (the admin
// endpoint failed to load, or a fresh install with no collections
// yet), so the allowlist stays usable either way.
export function KnowledgePicker({
  value,
  onChange,
}: {
  value: string[]
  onChange: (v: string[]) => void
}) {
  const collections = useKbCollections()
  const [open, setOpen] = useState(false)

  const toggle = (name: string) => {
    onChange(value.includes(name) ? value.filter((v) => v !== name) : [...value, name])
  }

  if (collections.length === 0) {
    return (
      <input
        value={value.join(', ')}
        onChange={(e) =>
          onChange(
            e.target.value
              .split(',')
              .map((s) => s.trim())
              .filter(Boolean),
          )
        }
        placeholder="product-docs, runbooks"
        className="mt-1.5 flex h-10 w-full rounded-lg border border-input bg-transparent px-3 text-sm outline-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
      />
    )
  }

  return (
    <div className="mt-1.5">
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button
            variant="outline"
            role="combobox"
            aria-expanded={open}
            className="h-10 w-full justify-between font-normal"
          >
            <span className="truncate text-left">
              {value.length === 0 ? 'No knowledge' : `${value.length} selected`}
            </span>
            <ChevronDownIcon className="size-4 shrink-0 opacity-50" />
          </Button>
        </PopoverTrigger>
        <PopoverContent className="w-[min(92vw,24rem)] p-0" align="start">
          <Command>
            <CommandInput placeholder="Search collections…" />
            <CommandList>
              <CommandEmpty>No collection matches.</CommandEmpty>
              <CommandGroup>
                {collections.map((c) => (
                  <CommandItem
                    key={c.name}
                    value={c.name}
                    data-checked={value.includes(c.name) || undefined}
                    onSelect={() => toggle(c.name)}
                  >
                    <div className="min-w-0 flex-1">
                      <div className="font-mono text-xs">{c.name}</div>
                      {c.description && (
                        <div className="truncate text-xs text-muted-foreground">
                          {c.description}
                        </div>
                      )}
                    </div>
                  </CommandItem>
                ))}
              </CommandGroup>
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
      {value.length > 0 && (
        <div className="mt-2 flex flex-wrap gap-1.5">
          {value.map((name) => (
            <button
              key={name}
              type="button"
              onClick={() => toggle(name)}
              className="rounded-full bg-muted px-2 py-0.5 font-mono text-xs text-muted-foreground transition hover:bg-destructive/10 hover:text-destructive"
              aria-label={`Remove ${name}`}
            >
              {name} ×
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
