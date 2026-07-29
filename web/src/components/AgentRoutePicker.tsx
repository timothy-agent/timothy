import {
  AiBrain01Icon,
  ArrowDown01Icon,
  SparklesIcon,
  Tick02Icon,
} from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useEffect, useState } from 'react'
import { listRoutes } from '../api/client'
import type { AdminRoute } from '../api/types'
import { AUTO_AGENT, useAgents } from './AgentPicker'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from './ui/dropdown-menu'

// Module-level cache: the picker renders in every composer; the route
// list changes rarely and a stale list self-heals on the next mount.
let cachedRoutes: AdminRoute[] | null = null

function useRoutes(): AdminRoute[] | null {
  const [routes, setRoutes] = useState<AdminRoute[] | null>(cachedRoutes)
  useEffect(() => {
    listRoutes().then(
      (list) => {
        cachedRoutes = list
        setRoutes(cachedRoutes)
      },
      () => undefined, // admin proxy may be unreachable — picker hides itself
    )
  }, [])
  return routes
}

// AgentRoutePicker combines who serves the next message with which
// model chain serves it (D-034) into one control: the agent carries
// its own routing, skills, tools, and memory behavior, while the route
// section lets a route override that per turn. The route section only
// renders once routes are fetched successfully — on failure it stays
// hidden rather than showing a broken picker.
export function AgentRoutePicker({
  agent,
  onAgent,
  route,
  onRoute,
}: {
  agent: string
  onAgent: (a: string) => void
  route?: string
  onRoute?: (r: string) => void
}) {
  const agents = useAgents()
  const routes = useRoutes()
  const isAuto = agent === AUTO_AGENT
  const current = isAuto
    ? null
    : (agents.find((a) => a.name === agent) ?? agents.find((a) => a.is_default) ?? null)
  const showRoutes = Boolean(onRoute) && routes !== null
  const isRouteAuto = !route
  const currentRoute = isRouteAuto ? null : (routes?.find((r) => r.name === route) ?? null)

  const agentLabel = isAuto ? 'Auto' : (current?.name ?? 'Agent')
  const label = currentRoute ? `${agentLabel} · ${currentRoute.name}` : agentLabel

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label="Agent and route"
        className="flex h-8 items-center gap-1.5 rounded-full border border-zinc-950/10 px-3 text-sm text-zinc-700 transition hover:bg-zinc-100 dark:border-white/10 dark:text-zinc-200 dark:hover:bg-zinc-700/50"
      >
        <HugeiconsIcon
          icon={isAuto && !currentRoute ? SparklesIcon : AiBrain01Icon}
          className="size-4"
        />
        <span className="capitalize">{label}</span>
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
          onSelect={() => onAgent(AUTO_AGENT)}
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
              onSelect={() => onAgent(a.name)}
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
        {showRoutes && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="px-2.5 pt-1.5">
              <div className="text-sm font-semibold">Route</div>
              <p className="mt-0.5 text-xs font-normal text-muted-foreground">
                Which model chain serves your next message. Routes are configured in Settings.
              </p>
            </DropdownMenuLabel>
            <DropdownMenuItem
              onSelect={() => onRoute?.('')}
              data-selected={isRouteAuto || undefined}
              className="items-start gap-3 rounded-lg px-2.5 py-2 data-selected:bg-zinc-100 dark:data-selected:bg-zinc-800"
            >
              <div className="min-w-0 flex-1">
                <span className="text-sm font-medium">Auto</span>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  Uses the agent's own route, or the server default.
                </p>
              </div>
              {isRouteAuto && <HugeiconsIcon icon={Tick02Icon} className="mt-1 size-4 shrink-0" />}
            </DropdownMenuItem>
            {routes?.map((r) => {
              const selected = r.name === route
              const chainLabel =
                r.chain.length > 0 ? r.chain.map((c) => c.model).join(' → ') : 'No models configured'
              return (
                <DropdownMenuItem
                  key={r.name}
                  onSelect={() => onRoute?.(r.name)}
                  data-selected={selected || undefined}
                  className="items-start gap-3 rounded-lg px-2.5 py-2 data-selected:bg-zinc-100 dark:data-selected:bg-zinc-800"
                >
                  <div className="min-w-0 flex-1">
                    <span className="text-sm font-medium capitalize">{r.name}</span>
                    <p className="mt-0.5 text-xs text-muted-foreground">{chainLabel}</p>
                  </div>
                  {selected && <HugeiconsIcon icon={Tick02Icon} className="mt-1 size-4 shrink-0" />}
                </DropdownMenuItem>
              )
            })}
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
