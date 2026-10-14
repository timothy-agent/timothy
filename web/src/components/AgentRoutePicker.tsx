import { Bot, Check, ChevronDown, ChevronLeft, ChevronRight, Search, Sparkles } from 'lucide-react'
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { buttonVariants } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'
import {
  AUTO_AGENT,
  type ProviderModelOption,
  useAgents,
  useProviderModels,
  useRoutes,
} from './AgentPicker'
import { ProviderLogo, logoPresetForName } from './timothy/provider-logo'
import { Popover, PopoverContent, PopoverTrigger } from './ui/popover'

type Panel = 'root' | 'agent' | 'route' | 'model'

// AgentRoutePicker combines who serves the next message with which
// model chain serves it (D-034) and an optional direct model pin
// (model_hint) into one control. Root rows drill into searchable
// flyouts: Agent, Route, and Model (providers → models), matching the
// grouped picker pattern used elsewhere for long option lists.
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
  const [open, setOpen] = useState(false)
  const [panel, setPanel] = useState<Panel>('root')
  const [modelProvider, setModelProvider] = useState<string | null>(null)
  const [query, setQuery] = useState('')

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

  const byProvider = useMemo(() => {
    const map = new Map<string, ProviderModelOption[]>()
    for (const m of models ?? []) {
      const list = map.get(m.provider_name) ?? []
      list.push(m)
      map.set(m.provider_name, list)
    }
    return map
  }, [models])

  const providerNames = useMemo(() => [...byProvider.keys()], [byProvider])

  useEffect(() => {
    if (!open) {
      setPanel('root')
      setModelProvider(null)
      setQuery('')
    }
  }, [open])

  // Prefer the provider that owns the current pin when opening Model.
  useEffect(() => {
    if (panel === 'model' && !modelProvider && currentModel) {
      setModelProvider(currentModel.provider_name)
    }
  }, [panel, modelProvider, currentModel])

  const q = query.trim().toLowerCase()

  const filteredAgents = useMemo(() => {
    if (!q) return agents
    return agents.filter(
      (a) =>
        a.name.toLowerCase().includes(q) ||
        (a.description ?? '').toLowerCase().includes(q),
    )
  }, [agents, q])

  const filteredRoutes = useMemo(() => {
    const enabled = (routes ?? []).filter((r) => r.enabled)
    if (!q) return enabled
    return enabled.filter((r) => {
      const chain = r.chain.map((c) => c.model).join(' ')
      return r.name.toLowerCase().includes(q) || chain.toLowerCase().includes(q)
    })
  }, [routes, q])

  const filteredProviders = useMemo(() => {
    if (!q) return providerNames
    return providerNames.filter((name) => {
      if (name.toLowerCase().includes(q)) return true
      return (byProvider.get(name) ?? []).some(
        (m) =>
          m.model.toLowerCase().includes(q) ||
          (m.display_name ?? '').toLowerCase().includes(q),
      )
    })
  }, [providerNames, byProvider, q])

  const activeProvider = modelProvider && byProvider.has(modelProvider)
    ? modelProvider
    : (filteredProviders[0] ?? null)

  const filteredModels = useMemo(() => {
    if (!activeProvider) return []
    const list = byProvider.get(activeProvider) ?? []
    if (!q) return list
    // When the query matches the provider name, show all its models.
    if (activeProvider.toLowerCase().includes(q)) return list
    return list.filter(
      (m) =>
        m.model.toLowerCase().includes(q) ||
        (m.display_name ?? '').toLowerCase().includes(q),
    )
  }, [activeProvider, byProvider, q])

  const go = (next: Panel) => {
    setQuery('')
    setPanel(next)
    if (next !== 'model') setModelProvider(null)
  }

  const closeAfter = (fn: () => void) => {
    fn()
    setOpen(false)
  }

  const modelSubtitle = currentModel
    ? (currentModel.display_name ?? currentModel.model)
    : isModelAuto
      ? 'Auto'
      : modelHint?.includes('/')
        ? modelHint.slice(modelHint.lastIndexOf('/') + 1)
        : 'Auto'

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
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
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className={cn(
          'p-0',
          panel === 'model' ? 'w-[min(92vw,36rem)]' : 'w-[min(92vw,22rem)]',
        )}
      >
        {panel === 'root' && (
          <div className="p-1.5">
            <div className="px-2.5 pt-1.5 pb-1 text-[0.65rem] font-medium tracking-wide text-muted-foreground uppercase">
              Select
            </div>
            <RootRow
              title="Agent"
              subtitle={agentLabel}
              onSelect={() => go('agent')}
            />
            {showRoutes && (
              <RootRow
                title="Route"
                subtitle={currentRoute?.name ?? 'Auto'}
                onSelect={() => go('route')}
              />
            )}
            {showModels && (
              <RootRow
                title="Model"
                subtitle={modelSubtitle}
                onSelect={() => go('model')}
              />
            )}
          </div>
        )}

        {panel === 'agent' && (
          <DrillPanel title="Agent" onBack={() => go('root')} query={query} onQuery={setQuery}>
            <SelectRow
              title="Auto"
              description="Picks the best-fit agent for each message."
              selected={isAuto}
              onSelect={() => closeAfter(() => onAgent(AUTO_AGENT))}
            />
            {filteredAgents.map((a) => {
              const selected = a.id === (current?.id ?? '')
              return (
                <SelectRow
                  key={a.id}
                  title={a.name}
                  description={a.description}
                  badge={a.is_default ? 'Default' : undefined}
                  selected={selected}
                  onSelect={() => closeAfter(() => onAgent(a.id))}
                />
              )
            })}
            {filteredAgents.length === 0 && q && (
              <p className="px-2.5 py-3 text-xs text-muted-foreground">No agents match.</p>
            )}
          </DrillPanel>
        )}

        {panel === 'route' && showRoutes && (
          <DrillPanel title="Route" onBack={() => go('root')} query={query} onQuery={setQuery}>
            <SelectRow
              title="Auto"
              description="Uses the agent's own route, or the server default."
              selected={isRouteAuto}
              onSelect={() => closeAfter(() => onRoute?.(''))}
            />
            {filteredRoutes.map((r) => {
              const selected = r.name === route
              const chainLabel =
                r.chain.length > 0 ? r.chain.map((c) => c.model).join(' → ') : 'No models configured'
              return (
                <SelectRow
                  key={r.name}
                  title={r.name}
                  description={chainLabel}
                  monoDescription
                  selected={selected}
                  onSelect={() => closeAfter(() => onRoute?.(r.name))}
                />
              )
            })}
            {filteredRoutes.length === 0 && q && (
              <p className="px-2.5 py-3 text-xs text-muted-foreground">No routes match.</p>
            )}
          </DrillPanel>
        )}

        {panel === 'model' && showModels && (
          <DrillPanel title="Model" onBack={() => go('root')} query={query} onQuery={setQuery}>
            <div className="flex max-h-80 min-h-0 flex-col sm:flex-row">
              <div className="min-h-0 flex-1 overflow-y-auto border-b border-border sm:border-r sm:border-b-0">
                <SelectRow
                  title="Auto"
                  description="First usable model on the chosen route."
                  selected={isModelAuto}
                  onSelect={() => closeAfter(() => onModelHint?.(''))}
                />
                {filteredProviders.map((name) => {
                  const selectedInGroup = currentModel?.provider_name === name
                  const active = activeProvider === name
                  const pinned = byProvider.get(name)?.find((m) => m.value === modelHint)
                  return (
                    <button
                      key={name}
                      type="button"
                      data-selected={active || undefined}
                      className={cn(
                        'flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left outline-none hover:bg-muted/60 data-selected:bg-muted',
                      )}
                      onClick={() => setModelProvider(name)}
                      onMouseEnter={() => setModelProvider(name)}
                    >
                      <ProviderLogo preset={logoPresetForName(name)} className="size-7" />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-sm font-medium">{name}</span>
                        {selectedInGroup && pinned && (
                          <span className="block truncate text-xs text-muted-foreground">
                            {pinned.display_name ?? pinned.model}
                          </span>
                        )}
                      </span>
                      <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />
                    </button>
                  )
                })}
                {filteredProviders.length === 0 && q && (
                  <p className="px-2.5 py-3 text-xs text-muted-foreground">No providers match.</p>
                )}
              </div>
              <div className="min-h-0 flex-1 overflow-y-auto">
                {activeProvider ? (
                  <>
                    <div className="px-2.5 pt-2 pb-1 text-xs font-medium text-muted-foreground">
                      {activeProvider}
                    </div>
                    {filteredModels.map((m) => {
                      const selected = m.value === modelHint
                      return (
                        <button
                          key={m.value}
                          type="button"
                          data-selected={selected || undefined}
                          className="flex w-full items-start gap-2.5 rounded-md px-2.5 py-2 text-left outline-none hover:bg-muted/60 data-selected:bg-muted"
                          onClick={() => closeAfter(() => onModelHint?.(m.value))}
                        >
                          <ProviderLogo preset={logoPresetForName(m.provider_name)} className="mt-0.5 size-7" />
                          <span className="min-w-0 flex-1">
                            <span className="block truncate font-mono text-sm font-medium">
                              {m.display_name ?? m.model}
                            </span>
                          </span>
                          {selected && <Check className="mt-1 size-4 shrink-0" />}
                        </button>
                      )
                    })}
                    {filteredModels.length === 0 && (
                      <p className="px-2.5 py-3 text-xs text-muted-foreground">No models match.</p>
                    )}
                  </>
                ) : (
                  <p className="px-2.5 py-3 text-xs text-muted-foreground">Pick a provider.</p>
                )}
              </div>
            </div>
          </DrillPanel>
        )}
      </PopoverContent>
    </Popover>
  )
}

function RootRow({
  title,
  subtitle,
  onSelect,
}: {
  title: string
  subtitle: string
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      aria-label={title}
      className="flex w-full items-center gap-3 rounded-md px-2.5 py-2 text-left outline-none hover:bg-muted/60"
      onClick={onSelect}
    >
      <span className="min-w-0 flex-1" aria-hidden="true">
        <span className="block text-sm font-medium">{title}</span>
        <span className="mt-0.5 block truncate text-xs text-muted-foreground capitalize">{subtitle}</span>
      </span>
      <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
    </button>
  )
}

function DrillPanel({
  title,
  onBack,
  query,
  onQuery,
  children,
}: {
  title: string
  onBack: () => void
  query: string
  onQuery: (q: string) => void
  children: ReactNode
}) {
  return (
    <div className="flex max-h-[min(70vh,28rem)] flex-col">
      <div className="flex items-center gap-1 border-b border-border px-1.5 py-1.5">
        <button
          type="button"
          aria-label="Back"
          className="grid size-8 place-items-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
          onClick={onBack}
        >
          <ChevronLeft className="size-4" />
        </button>
        <span className="text-sm font-semibold">{title}</span>
      </div>
      <div className="border-b border-border p-2">
        <div className="relative">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            size="sm"
            value={query}
            onChange={(e) => onQuery(e.target.value)}
            placeholder={`Search ${title.toLowerCase()}…`}
            className="pl-8"
            aria-label={`Search ${title.toLowerCase()}`}
          />
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-1.5">{children}</div>
    </div>
  )
}

function SelectRow({
  title,
  description,
  monoDescription,
  badge,
  selected,
  onSelect,
}: {
  title: string
  description?: string
  monoDescription?: boolean
  badge?: string
  selected?: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      data-selected={selected || undefined}
      className="flex w-full items-start gap-3 rounded-md px-2.5 py-2 text-left outline-none hover:bg-muted/60 data-selected:bg-muted"
      onClick={onSelect}
    >
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-2">
          <span className="text-sm font-medium capitalize">{title}</span>
          {badge && <Badge variant="outline">{badge}</Badge>}
        </span>
        {description && (
          <span
            className={cn(
              'mt-0.5 block text-xs text-muted-foreground',
              monoDescription && 'font-mono',
            )}
          >
            {description}
          </span>
        )}
      </span>
      {selected && <Check className="mt-1 size-4 shrink-0" />}
    </button>
  )
}
