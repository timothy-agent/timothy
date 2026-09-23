import type { AdminRoute, Destination, MissionTemplate } from '../../api/types'
import { CURRENCIES } from '../../lib/currencies'
import { type PendingAttachment } from '../Composer'
import { envIcon } from '../icons/EnvIcons'
import { Field } from '../timothy/field'
import { SegmentedControl } from '../timothy/segmented-control'
import { Checkbox } from '../ui/checkbox'
import { Input } from '../ui/input'
import { Label } from '../ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Textarea } from '../ui/textarea'
import { MissionAttachments } from './MissionAttachments'
import {
  ENVIRONMENT_AUTO,
  environmentChoices,
  EXECUTOR_DEFAULT,
  executorChoices,
  reviewHarnessChoices,
  ROUTE_DEFAULT,
} from './MissionForm'

const numberOrUndefined = (v: string) => (v === '' ? undefined : Number(v))

function RouteSelect({
  label,
  value,
  defaultLabel,
  routes,
  onChange,
}: {
  label: string
  value: string | undefined
  defaultLabel: string
  routes: AdminRoute[] | null
  onChange: (v: string) => void
}) {
  return (
    <Field label={label} optional>
      {(p) =>
        routes === null ? (
          <Input {...p} value={value ?? ''} onChange={(e) => onChange(e.target.value)} placeholder="default" />
        ) : (
          <Select value={value || ROUTE_DEFAULT} onValueChange={(v) => onChange(v === ROUTE_DEFAULT ? '' : v)}>
            <SelectTrigger {...p} className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ROUTE_DEFAULT}>{defaultLabel}</SelectItem>
              {routes
                .filter((r) => r.enabled)
                .map((r) => (
                  <SelectItem key={r.name} value={r.name}>
                    {r.name}
                  </SelectItem>
                ))}
            </SelectContent>
          </Select>
        )
      }
    </Field>
  )
}

function CheckField({
  id,
  label,
  description,
  checked,
  onChange,
}: {
  id: string
  label: string
  description: string
  checked: boolean
  onChange: (v: boolean) => void
}) {
  return (
    <div className="flex items-start gap-2">
      <Checkbox id={id} checked={checked} onCheckedChange={(v) => onChange(v === true)} className="mt-0.5" />
      <div>
        <Label htmlFor={id}>{label}</Label>
        <p className="text-xs text-muted-foreground">{description}</p>
      </div>
    </div>
  )
}

// MissionActionFields edits the mission an automation run starts: a
// controlled MissionTemplate plus its attachments.
export function MissionActionFields({
  value,
  onChange,
  routes,
  destinations,
  attachments,
  onAttachmentsChange,
  goalError,
}: {
  value: MissionTemplate
  onChange: (t: MissionTemplate) => void
  routes: AdminRoute[] | null
  destinations: Destination[] | null
  attachments: PendingAttachment[]
  onAttachmentsChange: (a: PendingAttachment[]) => void
  goalError?: string
}) {
  const set = (patch: Partial<MissionTemplate>) => onChange({ ...value, ...patch })
  const coding = value.kind === 'coding'
  const picked = value.destination_ids ?? []
  const visibleDestinations = (destinations ?? []).filter((d) => d.enabled || picked.includes(d.id))

  return (
    <div className="space-y-5">
      <Field
        label="Goal"
        description="Use {{event.pr_url}} or {{notes.name}} to insert trigger data or notes."
        error={goalError}
      >
        <Textarea
          value={value.goal}
          onChange={(e) => set({ goal: e.target.value })}
          rows={8}
          placeholder="What should each run do? Markdown works."
          className="min-h-40 resize-y"
        />
      </Field>

      <div className="space-y-2">
        <p className="text-sm leading-5 font-semibold">Kind</p>
        <SegmentedControl
          aria-label="Mission kind"
          value={value.kind}
          onChange={(v) => set({ kind: v as MissionTemplate['kind'], light: v === 'coding' ? undefined : value.light })}
          options={[
            { value: 'general', label: 'General' },
            { value: 'coding', label: 'Coding' },
          ]}
        />
      </div>

      {!coding && (
        <CheckField
          id="action-light"
          label="Light mission"
          description="One pass with no discover, plan or review. The final message is the result."
          checked={!!value.light}
          onChange={(light) => set({ light })}
        />
      )}
      <CheckField
        id="action-auto-approve"
        label="Auto-approve safe tool calls"
        description="Runs without pausing on routine commands. Destructive or unknown commands still ask."
        checked={value.auto_approve_tools ?? true}
        onChange={(auto_approve_tools) => set({ auto_approve_tools })}
      />

      {coding && (
        <div className="grid gap-5 sm:grid-cols-2">
          <Field label="Harness" optional>
            {(p) => (
              <Select
                value={value.harness || EXECUTOR_DEFAULT}
                onValueChange={(v) => set({ harness: v === EXECUTOR_DEFAULT ? '' : v })}
              >
                <SelectTrigger {...p} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {executorChoices.map((c) => (
                    <SelectItem key={c.value} value={c.value}>
                      {c.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </Field>
          <Field label="Environment" optional>
            {(p) => (
              <Select
                value={value.environment || ENVIRONMENT_AUTO}
                onValueChange={(v) => set({ environment: v === ENVIRONMENT_AUTO ? '' : v })}
              >
                <SelectTrigger {...p} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {environmentChoices.map((c) => {
                    const EnvIcon = envIcon(c.value)
                    return (
                      <SelectItem key={c.value} value={c.value}>
                        {EnvIcon && <EnvIcon />}
                        {c.label}
                      </SelectItem>
                    )
                  })}
                </SelectContent>
              </Select>
            )}
          </Field>
        </div>
      )}

      <div className="grid gap-5 sm:grid-cols-2">
        <RouteSelect label="Route" value={value.route} defaultLabel="Default" routes={routes} onChange={(route) => set({ route })} />
        <RouteSelect
          label="Plan route"
          value={value.plan_route}
          defaultLabel="Same as build route"
          routes={routes}
          onChange={(plan_route) => set({ plan_route })}
        />
        <RouteSelect
          label="Review route"
          value={value.review_route}
          defaultLabel="Same as plan route"
          routes={routes}
          onChange={(review_route) => set({ review_route })}
        />
        {!value.light && (
          <Field label="Review harness" optional>
            {(p) => (
              <Select
                value={value.review_harness || EXECUTOR_DEFAULT}
                onValueChange={(v) => set({ review_harness: v === EXECUTOR_DEFAULT ? '' : v })}
              >
                <SelectTrigger {...p} className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {reviewHarnessChoices.map((c) => (
                    <SelectItem key={c.value} value={c.value}>
                      {c.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </Field>
        )}
      </div>

      <div className="grid gap-5 sm:grid-cols-2">
        <Field label="Max iterations" optional>
          <Input
            type="number"
            min={1}
            value={value.max_iterations ?? ''}
            onChange={(e) => set({ max_iterations: numberOrUndefined(e.target.value) })}
            placeholder="Default"
          />
        </Field>
        <Field label="Budget per run" optional>
          {(p) => (
            <div className="flex gap-2">
              <Input
                {...p}
                type="number"
                min={0}
                step="any"
                value={value.budget_amount ?? ''}
                onChange={(e) => set({ budget_amount: numberOrUndefined(e.target.value) })}
                placeholder="No limit"
                className="flex-1"
              />
              <Select value={value.budget_currency || 'USD'} onValueChange={(budget_currency) => set({ budget_currency })}>
                <SelectTrigger className="w-24" aria-label="Budget currency">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {CURRENCIES.map((c) => (
                    <SelectItem key={c} value={c}>
                      {c}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
        </Field>
      </div>

      {visibleDestinations.length > 0 && (
        <div role="group" aria-label="Destinations" className="space-y-2">
          <p className="text-sm leading-5 font-semibold">Destinations</p>
          <p className="text-sm text-muted-foreground">Where each run delivers its result.</p>
          <div className="space-y-2">
            {visibleDestinations.map((d) => (
              <div key={d.id} className="flex items-center gap-2">
                <Checkbox
                  id={`action-destination-${d.id}`}
                  checked={picked.includes(d.id)}
                  onCheckedChange={(v) =>
                    set({ destination_ids: v === true ? [...picked, d.id] : picked.filter((id) => id !== d.id) })
                  }
                />
                <Label htmlFor={`action-destination-${d.id}`} className="font-normal">
                  {d.name}
                </Label>
                <span className="text-xs text-muted-foreground">{d.kind}</span>
                {!d.enabled && <span className="text-xs text-destructive">disabled, enable it in Settings to use it</span>}
              </div>
            ))}
          </div>
        </div>
      )}

      <MissionAttachments attachments={attachments} onChange={onAttachmentsChange} />
    </div>
  )
}
