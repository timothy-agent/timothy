import { Bot, Check, ChevronDown, Sparkles } from 'lucide-react'
import { buttonVariants } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import {
  AUTO_AGENT,
  type ProviderModelOption,
  useAgents,
  useProviderModels,
  useRoutes,
} from './AgentPicker'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from './ui/dropdown-menu'

// AgentRoutePicker combines who serves the next message with which
// model chain serves it (D-034) and an optional direct model pin
// (model_hint) into one control: the agent carries its own routing,
// skills, tools, and memory behavior, while the route section lets a
// route override that per turn and the model section pins one
// Provider/model without editing the route chain. Route/Model sections
// only render once their lists fetch successfully — on failure they
// stay hidden rather than showing a broken picker.
export function AgentRoutePicker({
  agent,
  onAgent,
  route,
  onRoute,
  modelHint,
  onModelHint,
}: {
  agent: string
  onAgent: (a: string) => void
  route?: string
  onRoute?: (r: string) => void
  modelHint?: string
  onModelHint?: (h: string) => void
}) {
  const agents = useAgents()
  const routes = useRoutes()
  const models = useProviderModels()
  const isAuto = agent === AUTO_AGENT
  const current = isAuto
    ? null
    : (agents.find((a) => a.id === agent) ?? agents.find((a) => a.is_default) ?? null)
  const showRoutes = Boolean(onRoute) && routes !== null
  const showModels = Boolean(onModelHint) && models !== null
  const isRouteAuto = !route
  const currentRoute = isRouteAuto
    ? null
    : (routes?.find((r) => r.name === route && r.enabled) ?? null)
  const isModelAuto = !modelHint
  const currentModel = isModelAuto
    ? null
    : (models?.find((m) => m.value === modelHint) ?? null)

  const agentLabel = isAuto ? 'Auto' : (current?.name ?? 'Agent')
  const parts = [agentLabel]
  if (currentRoute) parts.push(currentRoute.name)
  if (currentModel) parts.push(currentModel.model)
  else if (modelHint && modelHint.includes('/')) parts.push(modelHint.slice(modelHint.lastIndexOf('/') + 1))
  const label = parts.join(' · ')

  // Group models by provider for scannable lists.
  const byProvider = new Map<string, ProviderModelOption[]>()
  for (const m of models ?? []) {
    const list = byProvider.get(m.provider_name) ?? []
    list.push(m)
    byProvider.set(m.provider_name, list)
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label="Agent, route and model"
        data-tour="chat.picker"
        className={cn(buttonVariants({ variant: 'ghost', size: 'sm' }))}
      >
        {isAuto && !currentRoute && !currentModel ? (
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
          const selected = a.id === (current?.id ?? '')
          return (
            <DropdownMenuItem
              key={a.id}
              onSelect={() => onAgent(a.id)}
              data-selected={selected || undefined}
              className="h-auto items-start gap-3 rounded-md px-2.5 py-2 data-selected:bg-muted"
            >
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium">{a.name}</span>
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
        {showModels && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="px-2.5 pt-1.5">
              <div className="text-sm font-semibold">Model</div>
              <p className="mt-0.5 text-xs font-normal text-muted-foreground">
                Pin one model for this turn. The route still supplies fallbacks and capability gates.
              </p>
            </DropdownMenuLabel>
            <DropdownMenuItem
              onSelect={() => onModelHint?.('')}
              data-selected={isModelAuto || undefined}
              className="h-auto items-start gap-3 rounded-md px-2.5 py-2 data-selected:bg-muted"
            >
              <div className="min-w-0 flex-1">
                <span className="text-sm font-medium">Auto</span>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  First usable model on the chosen route.
                </p>
              </div>
              {isModelAuto && <Check className="mt-1 size-4 shrink-0" />}
            </DropdownMenuItem>
            {[...byProvider.entries()].map(([provider, list]) => (
              <div key={provider}>
                <div className="px-2.5 pt-2 pb-0.5 text-xs font-medium text-muted-foreground">
                  {provider}
                </div>
                {list.map((m) => {
                  const selected = m.value === modelHint
                  return (
                    <DropdownMenuItem
                      key={m.value}
                      onSelect={() => onModelHint?.(m.value)}
                      data-selected={selected || undefined}
                      className="h-auto items-start gap-3 rounded-md px-2.5 py-2 data-selected:bg-muted"
                    >
                      <div className="min-w-0 flex-1">
                        <span className="text-sm font-medium font-mono">{m.display_name ?? m.model}</span>
                      </div>
                      {selected && <Check className="mt-1 size-4 shrink-0" />}
                    </DropdownMenuItem>
                  )
                })}
              </div>
            ))}
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
