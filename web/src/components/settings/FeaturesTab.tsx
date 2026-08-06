import { useCallback, useEffect, useState } from 'react'
import { getSettings, listRoutes, patchSettings, patchSettingValues } from '../../api/client'
import type { AdminRoute } from '../../api/types'
import { CURRENCIES } from '../../lib/currencies'
import { getNotificationSoundEnabled, setNotificationSoundEnabled } from '../../lib/sound'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { ErrorBanner, Toggle } from './shared'
import { errText } from './util'

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
      {values && <DefaultCurrencyCard values={values} onError={setError} onSaved={refresh} />}
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

const SENSITIVE_ROUTE_OFF = '__off__'

// SensitiveRouteCard picks the route sensitive-tool turns (e.g. reading
// email content) and their memory extraction/compaction side-calls pin
// to. Fetches the route list on mount like AgentAdd/AgentEdit; if that
// fetch fails (or the admin proxy is nil-gated), falls back to a plain
// text input so the setting stays editable without the picker.
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
              className="w-56"
              aria-label="Sensitive tool route"
            />
          </label>
        )}
        <Button onClick={save}>Save</Button>
        {saved && <span className="text-xs text-muted-foreground">Saved.</span>}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        Turns that read email content switch to this route, and their memory extraction and
        compaction follow. Chain it to a local provider for a privacy floor.
      </p>
    </div>
  )
}

