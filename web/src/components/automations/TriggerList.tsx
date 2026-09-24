import { Plus, Trash2 } from 'lucide-react'

import type { AdminConnector, AutomationTrigger, Channel } from '../../api/types'
import { cronPresets, presetFor } from '../../lib/cron'
import { Field } from '../timothy/field'
import { IconButton } from '../timothy/icon-button'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Switch } from '../ui/switch'
import { ChannelFields, ConnectorEventFields, ToolAllowlistField, WebhookFields } from './TriggerFields'
import { cronError, defaultCron, maxTriggers, newTriggerDraft, triggerError, type TriggerDraft } from './triggerDrafts'

const kindOptions: { value: AutomationTrigger['kind']; label: string }[] = [
  { value: 'cron', label: 'Cron' },
  { value: 'manual', label: 'Manual' },
  { value: 'connector_event', label: 'Connector event' },
  { value: 'webhook', label: 'Webhook' },
  { value: 'channel', label: 'Channel message' },
]

const secretMissing = (d: TriggerDraft) => d.kind === 'webhook' && d.credentialRef.trim() === ''

// TriggerList edits one to five triggers. errors holds server messages
// keyed by draft key; connectors feeds the connector event picker and
// channels the channel picker.
export function TriggerList({
  value,
  onChange,
  errors = {},
  connectors = null,
  channels = null,
  submitted = false,
}: {
  value: TriggerDraft[]
  onChange: (next: TriggerDraft[]) => void
  errors?: Record<string, string>
  connectors?: AdminConnector[] | null
  channels?: Channel[] | null
  submitted?: boolean
}) {
  const update = (key: string, patch: Partial<TriggerDraft>) =>
    onChange(value.map((d) => (d.key === key ? { ...d, ...patch } : d)))

  return (
    <div className="space-y-4">
      {value.map((d, i) => {
        const preset = presetFor(d.expr.trim())
        return (
          <div key={d.key} role="group" aria-label={`Trigger ${i + 1}`} className="space-y-5 rounded-md border border-border p-5">
            <div className="flex items-center justify-between gap-4">
              <p className="text-sm font-semibold">Trigger {i + 1}</p>
              <div className="flex items-center gap-2">
                <Switch
                  checked={d.enabled}
                  onCheckedChange={(enabled) => update(d.key, { enabled })}
                  aria-label={`Trigger ${i + 1} enabled`}
                />
                <IconButton
                  label={`Remove trigger ${i + 1}`}
                  icon={Trash2}
                  size="sm"
                  disabled={value.length <= 1}
                  onClick={() => onChange(value.filter((x) => x.key !== d.key))}
                />
              </div>
            </div>
            <div className="grid gap-5 sm:grid-cols-2">
              <Field label="Kind">
                {(p) => (
                  <Select
                    value={d.kind}
                    onValueChange={(v) => {
                      const kind = v as TriggerDraft['kind']
                      update(d.key, { kind, expr: kind === 'cron' && !d.expr ? defaultCron : d.expr })
                    }}
                  >
                    <SelectTrigger {...p} className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {kindOptions.map((o) => (
                        <SelectItem key={o.value} value={o.value}>
                          {o.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </Field>
              {d.kind === 'cron' && (
                <Field label="Repeats">
                  {(p) => (
                    <Select
                      value={preset}
                      onValueChange={(v) => {
                        const found = cronPresets.find((c) => c.value === v)
                        if (found?.cron) update(d.key, { expr: found.cron })
                      }}
                    >
                      <SelectTrigger {...p} className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {cronPresets.map((c) => (
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
            {d.kind === 'cron' && (
              <Field
                label="Cron expression"
                description="Runs in the timezone set under Settings, Features."
                error={errors[d.key] ?? cronError(d)}
              >
                <Input
                  value={d.expr}
                  onChange={(e) => update(d.key, { expr: e.target.value })}
                  placeholder="0 7 * * *"
                  className="font-mono"
                />
              </Field>
            )}
            {d.kind === 'manual' && (
              <p className="text-sm text-muted-foreground">Runs only when you press Run now.</p>
            )}
            {d.kind === 'connector_event' && (
              <ConnectorEventFields draft={d} update={(patch) => update(d.key, patch)} connectors={connectors} />
            )}
            {d.kind === 'channel' && (
              <ChannelFields draft={d} update={(patch) => update(d.key, patch)} channels={channels} />
            )}
            {d.kind === 'webhook' && (
              <WebhookFields
                draft={d}
                update={(patch) => update(d.key, patch)}
                error={submitted && secretMissing(d) ? triggerError(d) : undefined}
              />
            )}
            {errors[d.key] && d.kind !== 'cron' && (
              <p role="alert" className="text-xs text-destructive">
                {errors[d.key]}
              </p>
            )}
            {submitted && !errors[d.key] && d.kind !== 'cron' && !secretMissing(d) && triggerError(d) && (
              <p role="alert" className="text-xs text-destructive">
                {triggerError(d)}
              </p>
            )}
            <ToolAllowlistField draft={d} update={(patch) => update(d.key, patch)} index={i} />
          </div>
        )
      })}
      <div className="flex items-center gap-3">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={value.length >= maxTriggers}
          onClick={() => onChange([...value, newTriggerDraft()])}
        >
          <Plus aria-hidden />
          Add trigger
        </Button>
        <span className="text-xs text-muted-foreground tabular-nums">
          {value.length} of {maxTriggers}
        </span>
      </div>
    </div>
  )
}
