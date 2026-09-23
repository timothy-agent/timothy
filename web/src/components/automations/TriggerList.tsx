import { Plus, Trash2 } from 'lucide-react'

import type { AutomationTrigger } from '../../api/types'
import { cronPresets, presetFor } from '../../lib/cron'
import { Field } from '../timothy/field'
import { IconButton } from '../timothy/icon-button'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Switch } from '../ui/switch'
import { cronError, defaultCron, maxTriggers, newTriggerDraft, type TriggerDraft } from './triggerDrafts'

const kindOptions: { value: AutomationTrigger['kind']; label: string; disabled?: boolean }[] = [
  { value: 'cron', label: 'Cron' },
  { value: 'manual', label: 'Manual' },
  { value: 'webhook', label: 'Webhook (Phase 2)', disabled: true },
  { value: 'connector_event', label: 'Connector event (Phase 2)', disabled: true },
  { value: 'channel', label: 'Channel message (Phase 2)', disabled: true },
]

// TriggerList edits one to five triggers. errors holds server messages
// keyed by draft key.
export function TriggerList({
  value,
  onChange,
  errors = {},
}: {
  value: TriggerDraft[]
  onChange: (next: TriggerDraft[]) => void
  errors?: Record<string, string>
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
                        <SelectItem key={o.value} value={o.value} disabled={o.disabled}>
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
