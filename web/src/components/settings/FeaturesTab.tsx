import { useCallback, useEffect, useState } from 'react'
import { ChevronDownIcon } from 'lucide-react'
import { getSettings, listRoutes, patchSettings, patchSettingValues } from '../../api/client'
import type { AdminRoute } from '../../api/types'
import { CURRENCIES } from '../../lib/currencies'
import { getNotificationSoundEnabled, setNotificationSoundEnabled } from '../../lib/sound'
import { Button } from '../ui/button'
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from '../ui/command'
import { Input } from '../ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '../ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { ErrorBanner, Toggle } from './shared'
import { errText } from './util'

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
      refresh()
    })
  }

  return (
    <div className="mt-6 space-y-3">
      <ErrorBanner message={error} />
      {Object.entries(featureCopy).map(([key, copy]) => (
        <div key={key} className="flex items-center gap-4 rounded-xl border border-border p-4">
          <div className="min-w-0 flex-1">
            <div className="text-sm font-medium">{copy.label}</div>
            <p className="mt-0.5 text-xs text-muted-foreground">{copy.description}</p>
          </div>
          <Toggle
            on={flags?.[key] ?? true}
            onChange={(v) => flip(key, v)}
            label={copy.label}
          />
        </div>
      ))}
      {values && <SensitiveRouteCard values={values} onError={setError} onSaved={refresh} />}
      {values && <TimezoneCard values={values} onError={setError} onSaved={refresh} />}
      {values && <DefaultCurrencyCard values={values} onError={setError} onSaved={refresh} />}
      {values && <DefaultCodingExecutorCard values={values} onError={setError} onSaved={refresh} />}
      {values && <ExecutorRunBudgetCard values={values} onError={setError} onSaved={refresh} />}
      {values && <ReviewTokenCeilingCard values={values} onError={setError} onSaved={refresh} />}
      {values && <GitBranchPatternCard values={values} onError={setError} onSaved={refresh} />}
      {values && <GitCommitStyleCard values={values} onError={setError} onSaved={refresh} />}
      <NotificationSoundCard />
    </div>
  )
}

// NotificationSoundCard toggles the synthesized beep that fires
// alongside the global permission-pending toast — localStorage-backed
// (lib/sound.ts), not a server setting, so it applies instantly with
// no save button.
function NotificationSoundCard() {
  const [enabled, setEnabled] = useState(() => getNotificationSoundEnabled())

  return (
    <div className="flex items-center gap-4 rounded-xl border border-border p-4">
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium">Notification sound</div>
        <p className="mt-0.5 text-xs text-muted-foreground">
          Plays a short beep when a permission ask needs your approval, wherever you are in the
          app.
        </p>
      </div>
      <Toggle
        on={enabled}
        onChange={(v) => {
          setEnabled(v)
          setNotificationSoundEnabled(v)
        }}
        label="Notification sound"
      />
    </div>
  )
}

// DefaultCurrencyCard picks the ISO 4217 code new budgets (missions,
// spend limits) default to when the user hasn't overridden it —
// mirrors SensitiveRouteCard's fetch-on-mount / save-button shape,
// minus the fallback-to-text-input path since the currency list is
// static, never fetched.
function DefaultCurrencyCard({
  values,
  onError,
  onSaved,
}: {
  values: Record<string, string>
  onError: (msg: string) => void
  onSaved: () => void
}) {
  const [currency, setCurrency] = useState(values.default_currency || 'USD')
  const [saved, setSaved] = useState(false)

  const save = () => {
    setSaved(false)
    patchSettingValues({ default_currency: currency })
      .then(() => {
        setSaved(true)
        onSaved()
      })
      .catch((err: unknown) => onError(errText(err)))
  }

  return (
    <div className="rounded-xl border border-border p-4">
      <div className="text-sm font-medium">Default currency</div>
      <div className="mt-3 flex flex-wrap items-end gap-3">
        <div className="grid gap-1 text-xs text-muted-foreground">
          <span>Currency</span>
          <Select value={currency} onValueChange={setCurrency}>
            <SelectTrigger className="h-10 w-56" aria-label="Default currency">
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
        <Button onClick={save}>Save</Button>
        {saved && <span className="text-xs text-muted-foreground">Saved.</span>}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        New mission budgets default to this currency unless overridden at creation time.
      </p>
    </div>
  )
}

const CODING_EXECUTOR_NATIVE = '__native__'

// DefaultCodingExecutorCard picks the delegated coding-CLI harness new
// coding missions default to when the mission itself doesn't specify
// one — mirrors DefaultCurrencyCard's shape, options are static since
// the known harnesses (claude-cli, pi, codex-cli, opencode, cursor-cli)
// besides native.
function DefaultCodingExecutorCard({
  values,
  onError,
  onSaved,
}: {
  values: Record<string, string>
  onError: (msg: string) => void
  onSaved: () => void
}) {
  const [executor, setExecutor] = useState(values.coding_executor ?? '')
  const [saved, setSaved] = useState(false)

  const save = () => {
    setSaved(false)
    patchSettingValues({ coding_executor: executor })
      .then(() => {
        setSaved(true)
        onSaved()
      })
      .catch((err: unknown) => onError(errText(err)))
  }

  return (
    <div className="rounded-xl border border-border p-4">
      <div className="text-sm font-medium">Default coding harness</div>
      <div className="mt-3 flex flex-wrap items-end gap-3">
        <div className="grid gap-1 text-xs text-muted-foreground">
          <span>Harness</span>
          <Select
            value={executor || CODING_EXECUTOR_NATIVE}
            onValueChange={(v) => setExecutor(v === CODING_EXECUTOR_NATIVE ? '' : v)}
          >
            <SelectTrigger className="h-10 w-56" aria-label="Default coding harness">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={CODING_EXECUTOR_NATIVE}>Native</SelectItem>
              <SelectItem value="claude-cli">Claude Code</SelectItem>
              <SelectItem value="pi">pi</SelectItem>
              <SelectItem value="codex-cli">Codex CLI</SelectItem>
              <SelectItem value="opencode">OpenCode</SelectItem>
              <SelectItem value="cursor-cli">Cursor CLI</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <Button onClick={save}>Save</Button>
        {saved && <span className="text-xs text-muted-foreground">Saved.</span>}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        New coding missions delegate to this harness unless overridden at creation time.
      </p>
    </div>
  )
}

// EXECUTOR_RUN_BUDGET_DEFAULT_MIN mirrors settings.DefaultExecutorRunBudget
// (8h) for the placeholder; the server applies the real default.
const EXECUTOR_RUN_BUDGET_DEFAULT_MIN = 480

// ExecutorRunBudgetCard sets the wall-clock cap for one delegated
// harness run (issue #498). A runaway backstop, not a watchdog: the
// idle timeout and cost budget stop a broken run, so this only needs
// to be larger than any healthy run.
function ExecutorRunBudgetCard({
  values,
  onError,
  onSaved,
}: {
  values: Record<string, string>
  onError: (msg: string) => void
  onSaved: () => void
}) {
  const [minutes, setMinutes] = useState(values.executor_run_budget_minutes ?? '')
  const [saved, setSaved] = useState(false)

  const save = () => {
    setSaved(false)
    patchSettingValues({ executor_run_budget_minutes: minutes.trim() })
      .then(() => {
        setSaved(true)
        onSaved()
      })
      .catch((err: unknown) => onError(errText(err)))
  }

  return (
    <div className="rounded-xl border border-border p-4">
      <div className="text-sm font-medium">Harness run budget</div>
      <div className="mt-3 flex flex-wrap items-end gap-3">
        <div className="grid gap-1 text-xs text-muted-foreground">
          <span>Minutes per run</span>
          <Input
            type="number"
            min={1}
            value={minutes}
            onChange={(e) => setMinutes(e.target.value)}
            placeholder={String(EXECUTOR_RUN_BUDGET_DEFAULT_MIN)}
            className="h-10 w-40"
            aria-label="Harness run budget minutes"
          />
        </div>
        <Button onClick={save}>Save</Button>
        {saved && <span className="text-xs text-muted-foreground">Saved.</span>}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        Wall-clock cap for one delegated coding run. Empty uses the default (8 hours). A run that
        stops producing output is killed by the 10-minute idle timeout regardless.
      </p>
    </div>
  )
}

// REVIEW_TOKEN_CEILING_DEFAULT mirrors settings.DefaultReviewTokenCeiling
// for the placeholder; the server applies the real default.
const REVIEW_TOKEN_CEILING_DEFAULT = 1_500_000

// ReviewTokenCeilingCard sets the input tokens one mission may spend on
// review turns before it parks on budget (D-097). 0 disables the ceiling.
function ReviewTokenCeilingCard({
  values,
  onError,
  onSaved,
}: {
  values: Record<string, string>
  onError: (msg: string) => void
  onSaved: () => void
}) {
  const [tokens, setTokens] = useState(values.mission_review_token_ceiling ?? '')
  const [saved, setSaved] = useState(false)

  const save = () => {
    setSaved(false)
    patchSettingValues({ mission_review_token_ceiling: tokens.trim() })
      .then(() => {
        setSaved(true)
        onSaved()
      })
      .catch((err: unknown) => onError(errText(err)))
  }

  return (
    <div className="rounded-xl border border-border p-4">
      <div className="text-sm font-medium">Review token ceiling</div>
      <div className="mt-3 flex flex-wrap items-end gap-3">
        <div className="grid gap-1 text-xs text-muted-foreground">
          <span>Input tokens per mission</span>
          <Input
            type="number"
            min={0}
            value={tokens}
            onChange={(e) => setTokens(e.target.value)}
            placeholder={String(REVIEW_TOKEN_CEILING_DEFAULT)}
            className="h-10 w-40"
            aria-label="Review token ceiling"
          />
        </div>
        <Button onClick={save}>Save</Button>
        {saved && <span className="text-xs text-muted-foreground">Saved.</span>}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        Input tokens a mission may spend on review turns, summed from the cost ledger before every
        review round; at the ceiling the mission pauses on budget. Empty uses the default (1.5M),
        0 disables the ceiling.
      </p>
    </div>
  )
}

// GitBranchPatternCard picks the default branch-name template new
// coding missions expand at provisioning time — mirrors
// DefaultCurrencyCard's shape, a free-text input since the template
// language (placeholders) isn't a fixed choice list.
function GitBranchPatternCard({
  values,
  onError,
  onSaved,
}: {
  values: Record<string, string>
  onError: (msg: string) => void
  onSaved: () => void
}) {
  const [pattern, setPattern] = useState(values.git_branch_pattern ?? '')
  const [saved, setSaved] = useState(false)

  const save = () => {
    setSaved(false)
    patchSettingValues({ git_branch_pattern: pattern.trim() })
      .then(() => {
        setSaved(true)
        onSaved()
      })
      .catch((err: unknown) => onError(errText(err)))
  }

  return (
    <div className="rounded-xl border border-border p-4">
      <div className="text-sm font-medium">Default branch pattern</div>
      <div className="mt-3 flex flex-wrap items-end gap-3">
        <label className="grid gap-1 text-xs text-muted-foreground">
          Pattern
          <Input
            value={pattern}
            onChange={(e) => setPattern(e.target.value)}
            placeholder="{type}/{slug}"
            className="h-10 w-72"
            aria-label="Default branch pattern"
          />
        </label>
        <Button onClick={save}>Save</Button>
        {saved && <span className="text-xs text-muted-foreground">Saved.</span>}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        Placeholders: <code>{'{type}'}</code> (fix/feat/docs/…), <code>{'{slug}'}</code> (from the
        goal), <code>{'{login}'}</code> (GitHub login, empty for non-github missions),{' '}
        <code>{'{date}'}</code> (YYYYMMDD). Empty uses <code>{'{type}/{slug}'}</code>.
      </p>
    </div>
  )
}

const COMMIT_STYLE_DEFAULT = '__default__'

// GitCommitStyleCard picks the default commit-message style new
// missions' unit commits use — mirrors DefaultCodingExecutorCard's
// shape, a fixed choice list.
function GitCommitStyleCard({
  values,
  onError,
  onSaved,
}: {
  values: Record<string, string>
  onError: (msg: string) => void
  onSaved: () => void
}) {
  const [style, setStyle] = useState(values.git_commit_style ?? '')
  const [saved, setSaved] = useState(false)

  const save = () => {
    setSaved(false)
    patchSettingValues({ git_commit_style: style })
      .then(() => {
        setSaved(true)
        onSaved()
      })
      .catch((err: unknown) => onError(errText(err)))
  }

  return (
    <div className="rounded-xl border border-border p-4">
      <div className="text-sm font-medium">Default commit style</div>
      <div className="mt-3 flex flex-wrap items-end gap-3">
        <div className="grid gap-1 text-xs text-muted-foreground">
          <span>Style</span>
          <Select
            value={style || COMMIT_STYLE_DEFAULT}
            onValueChange={(v) => setStyle(v === COMMIT_STYLE_DEFAULT ? '' : v)}
          >
            <SelectTrigger className="h-10 w-56" aria-label="Default commit style">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={COMMIT_STYLE_DEFAULT}>Conventional (default)</SelectItem>
              <SelectItem value="plain">Plain</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <Button onClick={save}>Save</Button>
        {saved && <span className="text-xs text-muted-foreground">Saved.</span>}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        Conventional: <code>type: subject</code>. Plain: the unit title as-is, no type prefix.
      </p>
    </div>
  )
}

const SENSITIVE_ROUTE_OFF = '__off__'

// SensitiveRouteCard picks the route a turn using a connector marked
// "sensitive" (e.g. gmail) and its memory extraction/compaction
// side-calls pin to. Fetches the route list on mount like
// AgentAdd/AgentEdit; if that fetch fails (or the admin proxy is
// nil-gated), falls back to a plain text input so the setting stays
// editable without the picker.
function SensitiveRouteCard({
  values,
  onError,
  onSaved,
}: {
  values: Record<string, string>
  onError: (msg: string) => void
  onSaved: () => void
}) {
  const [route, setRoute] = useState(values.sensitive_tool_route ?? '')
  const [routes, setRoutes] = useState<AdminRoute[] | null>(null)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    listRoutes().then(setRoutes, () => setRoutes(null))
  }, [])

  const save = () => {
    setSaved(false)
    patchSettingValues({ sensitive_tool_route: route.trim() })
      .then(() => {
        setSaved(true)
        onSaved()
      })
      .catch((err: unknown) => onError(errText(err)))
  }

  return (
    <div className="rounded-xl border border-border p-4">
      <div className="text-sm font-medium">Sensitive tool route</div>
      <div className="mt-3 flex flex-wrap items-end gap-3">
        {routes ? (
          <div className="grid gap-1 text-xs text-muted-foreground">
            <span>Route</span>
            <Select
              value={route || SENSITIVE_ROUTE_OFF}
              onValueChange={(v) => setRoute(v === SENSITIVE_ROUTE_OFF ? '' : v)}
            >
              <SelectTrigger className="h-10 w-56" aria-label="Sensitive tool route">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={SENSITIVE_ROUTE_OFF}>Off (default)</SelectItem>
                {routes.map((r) => (
                  <SelectItem key={r.name} value={r.name}>
                    {r.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        ) : (
          <label className="grid gap-1 text-xs text-muted-foreground">
            Route
            <Input
              value={route}
              onChange={(e) => setRoute(e.target.value)}
              placeholder="empty = off"
              className="h-10 w-56"
              aria-label="Sensitive tool route"
            />
          </label>
        )}
        <Button onClick={save}>Save</Button>
        {saved && <span className="text-xs text-muted-foreground">Saved.</span>}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        Turns that use a connector marked "sensitive" (toggle it on that connector's own settings)
        switch to this route, and their memory extraction and compaction follow. Chain it to a
        local provider for a privacy floor.
      </p>
    </div>
  )
}

// TimezoneCard picks the IANA timezone destination deliveries (email,
// telegram) render completion timestamps in: mirrors KnowledgePicker's
// combobox-with-typeahead shape (Command + Popover), single-select
// instead of multi. Empty means UTC (settings.Store.Location's
// fallback).
function TimezoneCard({
  values,
  onError,
  onSaved,
}: {
  values: Record<string, string>
  onError: (msg: string) => void
  onSaved: () => void
}) {
  const [timezone, setTimezone] = useState(values.timezone ?? '')
  const [saved, setSaved] = useState(false)
  const [open, setOpen] = useState(false)
  const [zones] = useState(listTimezones)

  const save = (next: string) => {
    setSaved(false)
    patchSettingValues({ timezone: next })
      .then(() => {
        setSaved(true)
        onSaved()
      })
      .catch((err: unknown) => onError(errText(err)))
  }

  const choose = (zone: string) => {
    setTimezone(zone)
    setOpen(false)
    save(zone)
  }

  return (
    <div className="rounded-xl border border-border p-4">
      <div className="text-sm font-medium">Timezone</div>
      <div className="mt-3 flex flex-wrap items-end gap-3">
        <div className="grid gap-1 text-xs text-muted-foreground">
          <span>Zone</span>
          <Popover open={open} onOpenChange={setOpen}>
            <PopoverTrigger asChild>
              <Button
                variant="outline"
                role="combobox"
                aria-expanded={open}
                aria-label="Timezone"
                className="h-10 w-56 justify-between font-normal"
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
                      <CommandItem
                        key={z}
                        value={z}
                        data-checked={timezone === z || undefined}
                        onSelect={() => choose(z)}
                      >
                        {z}
                      </CommandItem>
                    ))}
                  </CommandGroup>
                </CommandList>
              </Command>
            </PopoverContent>
          </Popover>
        </div>
        {saved && <span className="text-xs text-muted-foreground">Saved.</span>}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        Dates and times everywhere follow this timezone: delivery timestamps, schedule cron
        times, and the current date shown to models. Empty defaults to UTC.
      </p>
    </div>
  )
}
