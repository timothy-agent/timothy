import type { ExecutionPlanEntry, ExecutionPlanPhase } from '../../api/types'
import { executorChoices } from './MissionForm'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { matchPreset } from '../settings/presets'
import { ProviderLogo } from '../settings/ProviderLogo'

// MODEL_AUTO is the select's sentinel for "no pin" — Radix Select
// rejects an empty-string item value, same convention MissionForm's
// route selects use for their own "Default" sentinel.
const MODEL_AUTO = '__auto__'

function pinValue(entry: ExecutionPlanEntry): string {
  return `${entry.provider_name}/${entry.model}`
}

// phaseLabels maps the API's phase key to its display name, in the
// fixed order the response always carries them.
const phaseLabels: Record<string, string> = {
  discover: 'Discover',
  plan: 'Plan',
  generate: 'Generate',
  prove: 'Prove',
  escalate: 'Escalate',
}

const phaseOrder = ['discover', 'plan', 'generate', 'prove', 'escalate']

// routeSourceLabels renders each provenance value human-readable, in
// parentheses next to the route name. 'off'/'none' render nothing —
// there's no route to attribute.
const routeSourceLabels: Record<string, string> = {
  explicit: 'set above',
  agent: 'from agent',
  'named-coding': 'coding route',
  'default-role': 'default route',
  'inherited-from-plan': 'same as plan route',
  'inherited-from-generate': 'same as generate route',
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

// modelPinFor/onModelPinChangeFor map a phase key to the pin state that
// backs it: generate uses route_model, discover/plan use plan_route_model
// (they share oversightRoute), prove uses review_route_model. Escalate
// is never pinned — it's a failure-path fallback (runner.go's
// workerModel clears route_model once escalated), so it gets no select.
function modelPinFor(phaseKey: string, props: MissionExecutionPlanProps): string {
  switch (phaseKey) {
    case 'discover':
    case 'plan':
      return props.planRouteModel
    case 'generate':
      return props.routeModel
    case 'prove':
      return props.reviewRouteModel
    default:
      return ''
  }
}

function onModelPinChangeFor(
  phaseKey: string,
  props: MissionExecutionPlanProps,
): ((v: string) => void) | null {
  switch (phaseKey) {
    case 'discover':
    case 'plan':
      return props.onPlanRouteModelChange
    case 'generate':
      return props.onRouteModelChange
    case 'prove':
      return props.onReviewRouteModelChange
    default:
      return null
  }
}

interface MissionExecutionPlanProps {
  plan: ExecutionPlanPhase[] | null
  routeModel: string
  onRouteModelChange: (v: string) => void
  planRouteModel: string
  onPlanRouteModelChange: (v: string) => void
  reviewRouteModel: string
  onReviewRouteModelChange: (v: string) => void
}

// MissionExecutionPlan renders the five-phase read-out of what a
// mission created with the current form state will actually run —
// the server-resolved consequence of the route/harness selects above
// it, plus a per-phase model pin (D-078): a select over that phase's
// entries, defaulting to "Auto (first usable)". Renders nothing while
// the plan hasn't loaded yet.
export function MissionExecutionPlan(props: MissionExecutionPlanProps) {
  const { plan } = props
  if (!plan || plan.length === 0) return null
  const byPhase = new Map(plan.map((p) => [p.phase, p]))

  return (
    <div className="rounded-lg border border-border p-4">
      <div className="text-sm font-semibold">This mission will run</div>
      <div className="mt-2 divide-y divide-border">
        {phaseOrder.map((key) => {
          const phase = byPhase.get(key)
          if (!phase) return null
          return <PhaseRow key={key} phase={phase} phaseKey={key} props={props} />
        })}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        Naming and memory extraction use the summarize route.
      </p>
    </div>
  )
}

function PhaseRow({
  phase,
  phaseKey,
  props,
}: {
  phase: ExecutionPlanPhase
  phaseKey: string
  props: MissionExecutionPlanProps
}) {
  const label = phaseLabels[phase.phase] ?? phase.phase
  const route = routeLabel(phase)
  const price = priceLabel(phase)
  const selected = phase.entries.find((e) => e.selected)
  const unusableCount = phase.entries.filter((e) => !e.usable).length
  const onChange = onModelPinChangeFor(phaseKey, props)

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
      {onChange && phase.entries.length > 0 ? (
        <ModelPinSelect
          label={`${label} model`}
          entries={phase.entries}
          pin={modelPinFor(phaseKey, props)}
          onChange={onChange}
          selected={selected}
        />
      ) : selected ? (
        <span className="flex items-center gap-1.5 font-medium">
          <ProviderLogo preset={matchPreset(selected)} className="size-4" />
          {selected.model}
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

// ModelPinSelect is the resolved-model cell for a pinnable phase: a
// select over that phase's entries, "Auto" plus every entry — unusable
// ones stay listed and disabled with their skip_reason as a tooltip,
// same pattern PhaseRow already used for display-only unusable counts.
// The Auto item names the entry it actually resolves to when one is
// selected and usable, so "Auto" isn't a mystery next to a picked model.
function ModelPinSelect({
  label,
  entries,
  pin,
  onChange,
  selected,
}: {
  label: string
  entries: ExecutionPlanEntry[]
  pin: string
  onChange: (v: string) => void
  selected: ExecutionPlanEntry | undefined
}) {
  return (
    <Select
      value={pin || MODEL_AUTO}
      onValueChange={(v) => onChange(v === MODEL_AUTO ? '' : v)}
    >
      <SelectTrigger aria-label={label} className="h-7 w-auto min-w-40 text-sm">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={MODEL_AUTO}>
          {selected && selected.usable ? (
            <>
              Auto
              <ProviderLogo preset={matchPreset(selected)} className="size-4" />
              {selected.model}
            </>
          ) : (
            'Auto (first usable)'
          )}
        </SelectItem>
        {entries.map((entry) => {
          const value = pinValue(entry)
          return (
            <SelectItem key={value} value={value} disabled={!entry.usable} title={entry.skip_reason || undefined}>
              <ProviderLogo preset={matchPreset(entry)} className="size-4" />
              {entry.model}
              {!entry.usable && entry.skip_reason ? ` - ${entry.skip_reason}` : ''}
            </SelectItem>
          )
        })}
      </SelectContent>
    </Select>
  )
}
