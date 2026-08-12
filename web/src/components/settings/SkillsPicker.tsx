import { useEffect, useState } from 'react'
import { ChevronDownIcon } from 'lucide-react'
import { listSkills } from '../../api/client'
import type { AdminSkill } from '../../api/types'
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
// skill surface changes rarely (a pack added/removed) and a stale
// list self-heals on the next mount.
let cachedSkills: AdminSkill[] | null = null

function useSkills(): AdminSkill[] {
  const [skills, setSkills] = useState<AdminSkill[]>(cachedSkills ?? [])
  useEffect(() => {
    listSkills().then(
      (list) => {
        cachedSkills = list
        setSkills(list)
      },
      () => undefined,
    )
  }, [])
  return skills
}

// SkillsPicker lets an agent's skills allowlist be built from the
// actual loaded packs instead of typed blind — the exact pack name is
// otherwise undiscoverable. Falls back to comma-separated free text
// when the list is empty (the admin endpoint failed to load, or a
// fresh install with no packs wired yet), so the allowlist stays
// usable either way.
export function SkillsPicker({
  value,
  onChange,
}: {
  value: string[]
  onChange: (v: string[]) => void
}) {
  const skills = useSkills()
  const [open, setOpen] = useState(false)

  const toggle = (name: string) => {
    onChange(value.includes(name) ? value.filter((v) => v !== name) : [...value, name])
  }

  if (skills.length === 0) {
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
        placeholder="research-brief, coding"
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
              {value.length === 0 ? 'No skills' : `${value.length} selected`}
            </span>
            <ChevronDownIcon className="size-4 shrink-0 opacity-50" />
          </Button>
        </PopoverTrigger>
        <PopoverContent className="w-[min(92vw,24rem)] p-0" align="start">
          <Command>
            <CommandInput placeholder="Search skills…" />
            <CommandList>
              <CommandEmpty>No skill matches.</CommandEmpty>
              <CommandGroup>
                {skills.map((s) => (
                  <CommandItem
                    key={s.name}
                    value={s.name}
                    data-checked={value.includes(s.name) || undefined}
                    onSelect={() => toggle(s.name)}
                  >
                    <div className="min-w-0 flex-1">
                      <div className="font-mono text-xs">{s.name}</div>
                      {s.description && (
                        <div className="truncate text-xs text-muted-foreground">
                          {s.description}
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
