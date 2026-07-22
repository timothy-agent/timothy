import {
  AiBrain01Icon,
  ArrowDown01Icon,
  SparklesIcon,
  Tick02Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useState } from 'react'
import { listAgents } from '../api/client'
import type { AdminAgent } from '../api/types'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from './ui/dropdown-menu'

// Sentinel value meaning "let the system pick" (D-034 follow-up) —
// matches the backend's autoAgentName. Not a real agent row: the
// picker synthesizes its entry, chat.Service resolves it per turn via
// agents.Dispatch before the normal agent lookup.
export const AUTO_AGENT = 'auto'

// Module-level cache: the picker renders in every composer; the agent
// list changes rarely and a stale list self-heals on the next mount.
let cachedAgents: AdminAgent[] | null = null

export function useAgents(): AdminAgent[] {
  const [agents, setAgents] = useState<AdminAgent[]>(cachedAgents ?? [])
  useEffect(() => {
    listAgents().then(
      (list) => {
        cachedAgents = list.filter((a) => a.enabled)
        setAgents(cachedAgents)
      },
      () => undefined,
    )
  }, [])
  return agents
}

// AgentPicker chooses WHO serves the next message (D-034). The agent
// carries its own routing, skills, tools, and memory behavior — the
// picker needs to explain none of that, just name and description.
export function AgentPicker({
  value,
  onChange,
}: {
  value: string
  onChange: (v: string) => void
}) {
  const agents = useAgents()
  const isAuto = value === AUTO_AGENT
  const current = isAuto
    ? null
    : (agents.find((a) => a.name === value) ?? agents.find((a) => a.is_default) ?? null)

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label="Agent"
        className="flex h-8 items-center gap-1.5 rounded-full border border-zinc-950/10 px-3 text-sm text-zinc-700 transition hover:bg-zinc-100 dark:border-white/10 dark:text-zinc-200 dark:hover:bg-zinc-700/50"
      >
        <HugeiconsIcon icon={isAuto ? SparklesIcon : AiBrain01Icon} className="size-4" />
        <span className="capitalize">{isAuto ? 'Auto' : (current?.name ?? 'Agent')}</span>
        <HugeiconsIcon icon={ArrowDown01Icon} className="size-3.5 opacity-60" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-[min(92vw,22rem)] p-1.5">
        <DropdownMenuLabel className="px-2.5 pt-1.5">
          <div className="text-sm font-semibold">Agent</div>
          <p className="mt-0.5 text-xs font-normal text-muted-foreground">
            Who serves your next message. Agents are configured in Settings.
          </p>
        </DropdownMenuLabel>
        <DropdownMenuItem
          onSelect={() => onChange(AUTO_AGENT)}
          data-selected={isAuto || undefined}
          className="items-start gap-3 rounded-lg px-2.5 py-2 data-selected:bg-zinc-100 dark:data-selected:bg-zinc-800"
        >
          <div className="min-w-0 flex-1">
            <span className="text-sm font-medium">Auto</span>
            <p className="mt-0.5 text-xs text-muted-foreground">
              Picks the best-fit agent for each message.
            </p>
          </div>
          {isAuto && <HugeiconsIcon icon={Tick02Icon} className="mt-1 size-4 shrink-0" />}
        </DropdownMenuItem>
        {agents.map((a) => {
          const selected = a.name === (current?.name ?? '')
          return (
            <DropdownMenuItem
              key={a.id}
              onSelect={() => onChange(a.name)}
              data-selected={selected || undefined}
              className="items-start gap-3 rounded-lg px-2.5 py-2 data-selected:bg-zinc-100 dark:data-selected:bg-zinc-800"
            >
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium capitalize">{a.name}</span>
                  {a.is_default && (
                    <span className="rounded bg-blue-500/10 px-1.5 py-px text-[10px] font-medium text-blue-600 dark:text-blue-400">
                      Default
                    </span>
                  )}
                </div>
                {a.description && (
                  <p className="mt-0.5 text-xs text-muted-foreground">{a.description}</p>
                )}
              </div>
              {selected && <HugeiconsIcon icon={Tick02Icon} className="mt-1 size-4 shrink-0" />}
            </DropdownMenuItem>
          )
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
