import { Bot, Check, ChevronDown, Sparkles } from 'lucide-react'
import { buttonVariants } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { AUTO_AGENT, useAgents, useRoutes } from './AgentPicker'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from './ui/dropdown-menu'

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
  const currentRoute = isRouteAuto
    ? null
    : (routes?.find((r) => r.name === route && r.enabled) ?? null)

  const agentLabel = isAuto ? 'Auto' : (current?.name ?? 'Agent')
  const label = currentRoute ? `${agentLabel} · ${currentRoute.name}` : agentLabel

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label="Agent and route"
        className={cn(buttonVariants({ variant: 'ghost', size: 'sm' }))}
      >
        {isAuto && !currentRoute ? (
          <Sparkles className="size-4 text-muted-foreground" />
        ) : (
          <Bot className="size-4 text-muted-foreground" />
        )}
        <span className="text-sm capitalize">{label}</span>
        <ChevronDown className="size-3.5 text-muted-foreground" />
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
          className="h-auto items-start gap-3 rounded-md px-2.5 py-2 data-selected:bg-muted"
        >
          <div className="min-w-0 flex-1">
            <span className="text-sm font-medium">Auto</span>
            <p className="mt-0.5 text-xs text-muted-foreground">
              Picks the best-fit agent for each message.
            </p>
          </div>
          {isAuto && <Check className="mt-1 size-4 shrink-0" />}
        </DropdownMenuItem>
        {agents.map((a) => {
          const selected = a.name === (current?.name ?? '')
          return (
            <DropdownMenuItem
              key={a.id}
              onSelect={() => onAgent(a.name)}
              data-selected={selected || undefined}
              className="h-auto items-start gap-3 rounded-md px-2.5 py-2 data-selected:bg-muted"
            >
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium capitalize">{a.name}</span>
                  {a.is_default && <Badge variant="outline">Default</Badge>}
                </div>
                {a.description && (
                  <p className="mt-0.5 text-xs text-muted-foreground">{a.description}</p>
                )}
              </div>
              {selected && <Check className="mt-1 size-4 shrink-0" />}
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
              className="h-auto items-start gap-3 rounded-md px-2.5 py-2 data-selected:bg-muted"
            >
              <div className="min-w-0 flex-1">
                <span className="text-sm font-medium">Auto</span>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  Uses the agent's own route, or the server default.
                </p>
              </div>
              {isRouteAuto && <Check className="mt-1 size-4 shrink-0" />}
            </DropdownMenuItem>
            {routes?.filter((r) => r.enabled).map((r) => {
              const selected = r.name === route
              const chainLabel =
                r.chain.length > 0 ? r.chain.map((c) => c.model).join(' → ') : 'No models configured'
              return (
                <DropdownMenuItem
                  key={r.name}
                  onSelect={() => onRoute?.(r.name)}
                  data-selected={selected || undefined}
                  className="h-auto items-start gap-3 rounded-md px-2.5 py-2 data-selected:bg-muted"
                >
                  <div className="min-w-0 flex-1">
                    <span className="text-sm font-medium capitalize">{r.name}</span>
                    <p className="mt-0.5 font-mono text-xs text-muted-foreground">{chainLabel}</p>
                  </div>
                  {selected && <Check className="mt-1 size-4 shrink-0" />}
                </DropdownMenuItem>
              )
            })}
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
