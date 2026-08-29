import { useEffect, useState } from 'react'
import { ChevronDownIcon } from 'lucide-react'
import { listTools } from '../../api/client'
import type { AdminTool } from '../../api/types'
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
// tool surface changes rarely (a connector enable/disable) and a
// stale list self-heals on the next mount.
let cachedTools: AdminTool[] | null = null

function useTools(): AdminTool[] {
  const [tools, setTools] = useState<AdminTool[]>(cachedTools ?? [])
  useEffect(() => {
    listTools().then(
      (list) => {
        cachedTools = list
        setTools(list)
      },
      () => undefined,
    )
  }, [])
  return tools
}

// ToolsPicker lets an agent's tools allowlist be built from the actual
// live tool surface (builtins + connector tools, e.g. a GitHub
// connector's "github_search_issues") instead of typed blind — the
// exact composed name a connector tool gets is otherwise undiscoverable.
// Falls back to comma-separated free text when the list is empty (the
// admin endpoint failed to load, or a fresh install with no tools
// wired yet), so the allowlist stays usable either way.
export function ToolsPicker({
  value,
  onChange,
}: {
  value: string[]
  onChange: (v: string[]) => void
}) {
  const tools = useTools()
  const [open, setOpen] = useState(false)

  const toggle = (name: string) => {
    onChange(value.includes(name) ? value.filter((v) => v !== name) : [...value, name])
  }

  if (tools.length === 0) {
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
        placeholder="search_web, fetch_url, shell"
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
              {value.length === 0 ? 'No tools' : `${value.length} selected`}
            </span>
            <ChevronDownIcon className="size-4 shrink-0 opacity-50" />
          </Button>
        </PopoverTrigger>
        <PopoverContent className="w-[min(92vw,24rem)] p-0" align="start">
          <Command>
            <CommandInput placeholder="Search tools…" />
            <CommandList>
              <CommandEmpty>No tool matches.</CommandEmpty>
              <CommandGroup>
                {tools.map((t) => (
                  <CommandItem
                    key={t.name}
                    value={t.name}
                    data-checked={value.includes(t.name) || undefined}
                    onSelect={() => toggle(t.name)}
                  >
                    <div className="min-w-0 flex-1">
                      <div className="font-mono text-xs">{t.name}</div>
                      {t.description && (
                        <div className="truncate text-xs text-muted-foreground">
                          {t.description}
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
