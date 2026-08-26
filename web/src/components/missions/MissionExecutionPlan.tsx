import type { ExecutionPlanPhase } from '../../api/types'
import { executorChoices } from './MissionForm'

// phaseLabels maps the API's phase key to its display name, in the
// fixed order the response always carries them.
const phaseLabels: Record<string, string> = {
  explore: 'Explore',
  plan: 'Plan',
  execute: 'Execute',
  review: 'Review',
  escalate: 'Escalate',
}

const phaseOrder = ['explore', 'plan', 'execute', 'review', 'escalate']

// routeSourceLabels renders each provenance value human-readable, in
// parentheses next to the route name. 'off'/'none' render nothing —
// there's no route to attribute.
const routeSourceLabels: Record<string, string> = {
  explicit: 'set above',
  agent: 'from agent',
  'named-coding': 'coding route',
  'default-role': 'default route',
  'inherited-from-plan': 'same as plan route',
  'inherited-from-execute': 'same as execute route',
}

function routeLabel(phase: ExecutionPlanPhase): string | null {
  if (!phase.route || phase.route_source === 'off' || phase.route_source === 'none') return null
  const provenance = routeSourceLabels[phase.route_source]
  return provenance ? `route: ${phase.route} (${provenance})` : `route: ${phase.route}`
}

// axisLabel names the axis column: "Native", or the harness's friendly
// label from MissionForm's own executorChoices mapping.
function axisLabel(phase: ExecutionPlanPhase): string {
  if (phase.axis !== 'harness') return 'Native'
  const known = executorChoices.find((c) => c.value === phase.harness)
  return known?.label ?? phase.harness
}

function formatPrice(n: number): string {
  return n % 1 === 0 ? String(n) : n.toFixed(2)
}

function priceLabel(phase: ExecutionPlanPhase): string | null {
  const selected = phase.entries.find((e) => e.selected)
  const prices = selected?.prices
  if (!prices || prices.input_per_mtok == null || prices.output_per_mtok == null) return null
  return `$${formatPrice(prices.input_per_mtok)}/$${formatPrice(prices.output_per_mtok)} per Mtok`
}

// MissionExecutionPlan renders the five-phase read-out of what a
// mission created with the current form state will actually run —
// the server-resolved consequence of the route/harness selects above
// it. Renders nothing while the plan hasn't loaded yet.
export function MissionExecutionPlan({ plan }: { plan: ExecutionPlanPhase[] | null }) {
  if (!plan || plan.length === 0) return null
  const byPhase = new Map(plan.map((p) => [p.phase, p]))

  return (
    <div className="rounded-lg border border-border p-4">
      <div className="text-sm font-semibold">This mission will run</div>
      <div className="mt-2 divide-y divide-border">
        {phaseOrder.map((key) => {
          const phase = byPhase.get(key)
          if (!phase) return null
          return <PhaseRow key={key} phase={phase} />
        })}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        Naming and memory extraction use the summarize route.
      </p>
    </div>
  )
}

function PhaseRow({ phase }: { phase: ExecutionPlanPhase }) {
  const label = phaseLabels[phase.phase] ?? phase.phase
  const route = routeLabel(phase)
  const price = priceLabel(phase)
  const selected = phase.entries.find((e) => e.selected)
  const unusableCount = phase.entries.filter((e) => !e.usable).length

  if (phase.skipped) {
    return (
      <div
        className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2 text-sm text-muted-foreground"
        title={phase.skip_reason || undefined}
      >
        <span className="w-20 shrink-0 font-medium">{label}</span>
        <span>Skipped{phase.skip_reason ? ` - ${phase.skip_reason}` : ''}</span>
      </div>
    )
  }

  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2 text-sm">
      <span className="w-20 shrink-0 font-medium">{label}</span>
      <span className="w-28 shrink-0 text-muted-foreground">{axisLabel(phase)}</span>
      {route && <span className="text-muted-foreground">{route}</span>}
      {selected ? (
        <span className="font-medium">
          {selected.provider_name} {selected.model}
        </span>
      ) : phase.entries.length > 0 ? (
        <span className="text-muted-foreground" title={phase.entries[0]?.skip_reason || undefined}>
          {unusableCount} unusable{phase.entries[0]?.skip_reason ? ` - ${phase.entries[0].skip_reason}` : ''}
        </span>
      ) : phase.skip_reason ? (
        <span className="text-muted-foreground" title={phase.skip_reason}>
          {phase.skip_reason}
        </span>
      ) : null}
      {price && <span className="ml-auto text-xs text-muted-foreground">{price}</span>}
    </div>
  )
}
