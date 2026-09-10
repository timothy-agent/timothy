import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { ChevronDownIcon } from 'lucide-react'
import { toast } from 'sonner'
import { getSettings, listRoutes, patchSettings, patchSettingValues } from '../../api/client'
import type { AdminRoute } from '../../api/types'
import { CURRENCIES } from '../../lib/currencies'
import { getNotificationSoundEnabled, setNotificationSoundEnabled } from '../../lib/sound'
import { Button } from '../ui/button'
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from '../ui/command'
import { Input } from '../ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '../ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Alert, AlertDescription } from '../ui/alert'
import { PageHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { settingsArea } from './settingsAreas'
import { UNSET } from './util'
import { errText } from '../../lib/errors'
import { SettingFlagRow } from './SettingFlagRow'
import { SettingValueCard } from './SettingValueCard'

const area = settingsArea('features')

// FALLBACK_TIMEZONES stands in for Intl.supportedValuesOf('timeZone')
// when that API is unavailable (older test environments): a short,
// common list, not an attempt to cover every IANA zone.
const FALLBACK_TIMEZONES = [
  'UTC',
  'Europe/Amsterdam',
  'Europe/London',
  'Europe/Berlin',
  'America/New_York',
  'America/Los_Angeles',
  'America/Chicago',
  'Asia/Kolkata',
  'Asia/Dhaka',
  'Asia/Tokyo',
  'Asia/Shanghai',
  'Australia/Sydney',
]

function listTimezones(): string[] {
  try {
    return Intl.supportedValuesOf('timeZone')
  } catch {
    return FALLBACK_TIMEZONES
  }
}

const featureCopy: Record<string, { label: string; description: string }> = {
  tools_enabled: {
    label: 'Tool execution',
    description: 'Off: chat answers as plain completion, no shell, no web fetch, no tool calls.',
  },
  memory_extraction_enabled: {
    label: 'Memory extraction',
    description: 'Off: turns stop feeding the long-term memory queue. Retrieval keeps working.',
  },
  compaction_enabled: {
    label: 'Compaction',
    description: 'Off: sessions grow unbounded until re-enabled. Useful when debugging context.',
  },
  scheduler_enabled: {
    label: 'Scheduler',
    description: 'Off: recurring schedules stop firing missions.',
  },
  kb_image_captioning_enabled: {
    label: 'KB image captioning',
    description: 'On: images in ingested documents get a vision-model caption, spending gateway tokens per image.',
  },
}

export function FeaturesTab() {
  const [flags, setFlags] = useState<Record<string, boolean> | null>(null)
  const [values, setValues] = useState<Record<string, string> | null>(null)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(() => {
    getSettings()
      .then((s) => {
        setFlags(s.settings)
        setValues(s.values)
        setError(null)
      })
      .catch((err: unknown) => setError(errText(err)))
  }, [])
  useEffect(refresh, [refresh])

  const flip = (key: string, value: boolean) => {
    setFlags((f) => (f ? { ...f, [key]: value } : f)) // optimistic
    patchSettings({ [key]: value }).catch((err: unknown) => {
      setError(errText(err))
      toast.error('Could not save', { description: errText(err) })
      refresh()
    })
  }

  return (
    <PageShell>
      <PageHeader
        title={area.label}
        description={area.description}
        breadcrumbs={[{ label: 'Settings', href: '/settings' }, { label: area.label }]}
      />
      <div className="space-y-4">
        {error && (
          <Alert tone="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        {Object.entries(featureCopy).map(([key, copy]) => (
          <SettingFlagRow
            key={key}
            id={key}
            label={copy.label}
            description={copy.description}
            checked={flags?.[key] ?? true}
            onChange={(v) => flip(key, v)}
          />
        ))}
        {values && <SensitiveRouteCard values={values} onSaved={refresh} />}
        {values && <TimezoneCard values={values} onSaved={refresh} />}
        {values && <PlainValueCards values={values} onSaved={refresh} />}
        <NotificationSoundCard />
      </div>
    </PageShell>
  )
}

// NotificationSoundCard toggles the synthesized beep that fires
// alongside the global permission-pending toast — localStorage-backed
// (lib/sound.ts), not a server setting, so it applies instantly with
// no save button.
function NotificationSoundCard() {
  const [enabled, setEnabled] = useState(() => getNotificationSoundEnabled())

  return (
    <SettingFlagRow
      id="notification_sound"
      label="Notification sound"
      description="Plays a short beep when a permission ask needs your approval, wherever you are in the app."
      checked={enabled}
      onChange={(v) => {
        setEnabled(v)
        setNotificationSoundEnabled(v)
      }}
    />
  )
}

// EXECUTOR_RUN_BUDGET_DEFAULT_MIN mirrors settings.DefaultExecutorRunBudget
// (8h) for the placeholder; the server applies the real default.
const EXECUTOR_RUN_BUDGET_DEFAULT_MIN = 480

// REVIEW_TOKEN_CEILING_DEFAULT mirrors settings.DefaultReviewTokenCeiling
// for the placeholder; the server applies the real default.
const REVIEW_TOKEN_CEILING_DEFAULT = 1_500_000

// MCP_TOOL_INDEX_THRESHOLD_DEFAULT mirrors
// settings.DefaultMCPToolIndexThreshold for the placeholder; the
// server applies the real default.
const MCP_TOOL_INDEX_THRESHOLD_DEFAULT = 8

// Descriptor for a plain value card: an Input or Select field, one
// settings key, and how its committed value maps to the field's
// working value. save() receives the trimmed working value.
interface ValueCardDescriptor {
  key: string
  title: string
  description: string
  render: (value: string, setValue: (v: string) => void) => ReactNode
  toPatchValue?: (value: string) => string
}

const valueCardDescriptors: ValueCardDescriptor[] = [
  {
    key: 'default_currency',
    title: 'Default currency',
    description: 'New mission budgets default to this currency unless overridden at creation time.',
    render: (value, setValue) => (
      <Select value={value || 'USD'} onValueChange={setValue}>
        <SelectTrigger className="h-9 w-56" aria-label="Default currency">
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
    ),
  },
  {
    key: 'coding_executor',
    title: 'Default coding harness',
    description: 'New coding missions delegate to this harness unless overridden at creation time.',
    render: (value, setValue) => (
      <Select value={value || UNSET} onValueChange={(v) => setValue(v === UNSET ? '' : v)}>
        <SelectTrigger className="h-9 w-56" aria-label="Default coding harness">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={UNSET}>Native</SelectItem>
          <SelectItem value="claude-cli">Claude Code</SelectItem>
          <SelectItem value="pi">pi</SelectItem>
          <SelectItem value="codex-cli">Codex CLI</SelectItem>
          <SelectItem value="opencode">OpenCode</SelectItem>
          <SelectItem value="cursor-cli">Cursor CLI</SelectItem>
        </SelectContent>
      </Select>
    ),
  },
  {
    key: 'executor_run_budget_minutes',
    title: 'Harness run budget',
    description:
      'Wall-clock cap for one delegated coding run. Empty uses the default (8 hours). A run that stops producing output is killed by the 10-minute idle timeout regardless.',
    render: (value, setValue) => (
      <Input
        type="number"
        min={1}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder={String(EXECUTOR_RUN_BUDGET_DEFAULT_MIN)}
        className="h-9 w-40"
        aria-label="Harness run budget minutes"
      />
    ),
    toPatchValue: (v) => v.trim(),
  },
  {
    key: 'mission_review_token_ceiling',
    title: 'Review token ceiling',
    description:
      'Input tokens a mission may spend on review turns, summed from the cost ledger before every review round; at the ceiling the mission pauses on budget. Empty uses the default (1.5M), 0 disables the ceiling.',
    render: (value, setValue) => (
      <Input
        type="number"
        min={0}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder={String(REVIEW_TOKEN_CEILING_DEFAULT)}
        className="h-9 w-40"
        aria-label="Review token ceiling"
      />
    ),
    toPatchValue: (v) => v.trim(),
  },
  {
    key: 'mcp_tool_index_threshold',
    title: 'MCP tool index threshold',
    description:
      'An MCP connector with more tools than this offers a one-line index plus a load_tool entry point instead of every schema, so a large server does not tax every turn. Empty uses the default (8), 0 keeps every connector eager.',
    render: (value, setValue) => (
      <Input
        type="number"
        min={0}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder={String(MCP_TOOL_INDEX_THRESHOLD_DEFAULT)}
        className="h-9 w-40"
        aria-label="MCP tool index threshold"
      />
    ),
    toPatchValue: (v) => v.trim(),
  },
  {
    key: 'git_branch_pattern',
    title: 'Default branch pattern',
    description:
      'Placeholders: {type} (fix/feat/docs/…), {slug} (from the goal), {login} (GitHub login, empty for non-github missions), {date} (YYYYMMDD). Empty uses {type}/{slug}.',
    render: (value, setValue) => (
      <Input
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder="{type}/{slug}"
        className="h-9 w-72"
        aria-label="Default branch pattern"
      />
    ),
    toPatchValue: (v) => v.trim(),
  },
  {
    key: 'git_commit_style',
    title: 'Default commit style',
    description: 'Conventional: type: subject. Plain: the unit title as-is, no type prefix.',
    render: (value, setValue) => (
      <Select value={value || UNSET} onValueChange={(v) => setValue(v === UNSET ? '' : v)}>
        <SelectTrigger className="h-9 w-56" aria-label="Default commit style">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={UNSET}>Conventional (default)</SelectItem>
          <SelectItem value="plain">Plain</SelectItem>
        </SelectContent>
      </Select>
    ),
  },
]

// PlainValueCards renders every value card whose control is an Input
// or Select from one descriptor array (section B), each an independent
// SettingValueCard: Save disabled until dirty, clean again on success.
function PlainValueCards({ values, onSaved }: { values: Record<string, string>; onSaved: () => void }) {
  return (
    <>
      {valueCardDescriptors.map((d) => (
        <ValueCard key={d.key} descriptor={d} baseline={values[d.key] ?? ''} onSaved={onSaved} />
      ))}
    </>
  )
}

function ValueCard({
  descriptor,
  baseline,
  onSaved,
}: {
  descriptor: ValueCardDescriptor
  baseline: string
  onSaved: () => void
}) {
  const [value, setValue] = useState(baseline)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const dirty = value !== baseline

  const save = () => {
    setSaving(true)
    setError(null)
    const patchValue = descriptor.toPatchValue ? descriptor.toPatchValue(value) : value
    patchSettingValues({ [descriptor.key]: patchValue })
      .then(() => {
        setSaving(false)
        toast.success('Saved')
        onSaved()
      })
      .catch((err: unknown) => {
        setSaving(false)
        setError(errText(err))
      })
  }

  return (
    <SettingValueCard
      title={descriptor.title}
      description={descriptor.description}
      dirty={dirty}
      saving={saving}
      error={error}
      onRetry={save}
      onSave={save}
    >
      {descriptor.render(value, setValue)}
    </SettingValueCard>
  )
}

// SensitiveRouteCard picks the route a turn using a connector marked
// "sensitive" (e.g. gmail) and its memory extraction/compaction
// side-calls pin to. Fetches the route list on mount like
// AgentAdd/AgentEdit; if that fetch fails (or the admin proxy is
// nil-gated), falls back to a plain text input so the setting stays
// editable without the picker.
function SensitiveRouteCard({ values, onSaved }: { values: Record<string, string>; onSaved: () => void }) {
  const baseline = values.sensitive_tool_route ?? ''
  const [route, setRoute] = useState(baseline)
  const [routes, setRoutes] = useState<AdminRoute[] | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const dirty = route !== baseline

  useEffect(() => {
    listRoutes().then(setRoutes, () => setRoutes(null))
  }, [])

  const save = () => {
    setSaving(true)
    setError(null)
    patchSettingValues({ sensitive_tool_route: route.trim() })
      .then(() => {
        setSaving(false)
        toast.success('Saved')
        onSaved()
      })
      .catch((err: unknown) => {
        setSaving(false)
        setError(errText(err))
      })
  }

  return (
    <SettingValueCard
      title="Sensitive tool route"
      description={`Turns that use a connector marked "sensitive" (toggle it on that connector's own settings) switch to this route, and their memory extraction and compaction follow. Chain it to a local provider for a privacy floor.`}
      dirty={dirty}
      saving={saving}
      error={error}
      onRetry={save}
      onSave={save}
    >
      {routes ? (
        <Select value={route || UNSET} onValueChange={(v) => setRoute(v === UNSET ? '' : v)}>
          <SelectTrigger className="h-9 w-56" aria-label="Sensitive tool route">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={UNSET}>Off (default)</SelectItem>
            {routes.map((r) => (
              <SelectItem key={r.name} value={r.name}>
                {r.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : (
        <Input
          value={route}
          onChange={(e) => setRoute(e.target.value)}
          placeholder="empty = off"
          className="h-9 w-56"
          aria-label="Sensitive tool route"
        />
      )}
    </SettingValueCard>
  )
}

// TimezoneCard picks the IANA timezone destination deliveries (email,
// telegram) render completion timestamps in: Combobox-with-typeahead
// shape (Command + Popover), single-select. Empty means UTC
// (settings.Store.Location's fallback). Immediate: an atomic
// preference, no Save button.
function TimezoneCard({ values, onSaved }: { values: Record<string, string>; onSaved: () => void }) {
  const [timezone, setTimezone] = useState(values.timezone ?? '')
  const [open, setOpen] = useState(false)
  const [zones] = useState(listTimezones)

  const save = (next: string) => {
    patchSettingValues({ timezone: next })
      .then(() => onSaved())
      .catch((err: unknown) => toast.error('Could not save timezone', { description: errText(err) }))
  }

  const choose = (zone: string) => {
    setTimezone(zone)
    setOpen(false)
    save(zone)
  }

  return (
    <SettingValueCard
      title="Timezone"
      description="Dates and times everywhere follow this timezone: delivery timestamps, schedule cron times, and the current date shown to models. Empty defaults to UTC."
      dirty={false}
    >
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button
            variant="outline"
            role="combobox"
            aria-expanded={open}
            aria-label="Timezone"
            className="h-9 w-56 justify-between font-normal"
          >
            <span className="truncate text-left">{timezone || 'UTC (default)'}</span>
            <ChevronDownIcon className="size-4 shrink-0 opacity-50" />
          </Button>
        </PopoverTrigger>
        <PopoverContent className="w-[min(92vw,20rem)] p-0" align="start">
          <Command>
            <CommandInput placeholder="Search timezones…" />
            <CommandList>
              <CommandEmpty>No timezone matches.</CommandEmpty>
              <CommandGroup>
                <CommandItem
                  value="UTC (default)"
                  data-checked={timezone === '' || undefined}
                  onSelect={() => choose('')}
                >
                  UTC (default)
                </CommandItem>
                {zones.map((z) => (
                  <CommandItem key={z} value={z} data-checked={timezone === z || undefined} onSelect={() => choose(z)}>
                    {z}
                  </CommandItem>
                ))}
              </CommandGroup>
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
    </SettingValueCard>
  )
}
