import { useCallback, useEffect, useState } from 'react'
import { getSettings, patchBudget, patchSettings, usageBudget } from '../../api/client'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { ErrorBanner, Toggle } from './shared'
import { errText } from './util'

const featureCopy: Record<string, { label: string; description: string }> = {
  tools_enabled: {
    label: 'Tool execution',
    description: 'Off: chat answers as plain completion — no shell, no web fetch, no tool calls.',
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
    description: 'Stored now; gains a consumer when the agent harness lands.',
  },
}

export function FeaturesTab() {
  const [flags, setFlags] = useState<Record<string, boolean> | null>(null)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(() => {
    getSettings()
      .then((s) => {
        setFlags(s)
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
      <BudgetsCard />
    </div>
  )
}

// BudgetsCard edits the gateway's spend budgets. Empty field = no
// budget for that window; both keys always travel so clearing works.
function BudgetsCard() {
  const [day, setDay] = useState('')
  const [month, setMonth] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    usageBudget()
      .then((b) => {
        setDay(b.day.limit_usd != null ? String(b.day.limit_usd) : '')
        setMonth(b.month.limit_usd != null ? String(b.month.limit_usd) : '')
      })
      .catch((err: unknown) => setError(errText(err)))
  }, [])

  // '' → null (clear), positive number → set, anything else → invalid.
  const parse = (v: string): number | null | undefined => {
    if (v.trim() === '') return null
    const n = Number(v)
    return Number.isFinite(n) && n > 0 ? n : undefined
  }

  const save = () => {
    setSaved(false)
    const d = parse(day)
    const m = parse(month)
    if (d === undefined || m === undefined) {
      setError('Budgets must be positive USD amounts (or empty for no budget).')
      return
    }
    patchBudget({ day: d, month: m })
      .then(() => {
        setError(null)
        setSaved(true)
      })
      .catch((err: unknown) => setError(errText(err)))
  }

  return (
    <div className="rounded-xl border border-border p-4">
      <div className="text-sm font-medium">Spend budgets</div>
      <p className="mt-0.5 text-xs text-muted-foreground">
        USD limits per UTC day and month. When spend reaches a limit the dashboard shows a
        banner; Prometheus gauges carry the same signal for external alerting. Requests are
        never blocked.
      </p>
      {error && (
        <div className="mt-3 rounded-lg border border-red-500/30 bg-red-500/5 p-2 text-xs text-red-600 dark:text-red-400">
          {error}
        </div>
      )}
      <div className="mt-3 flex flex-wrap items-end gap-3">
        <label className="grid gap-1 text-xs text-muted-foreground">
          Daily (USD)
          <Input
            value={day}
            onChange={(e) => setDay(e.target.value)}
            placeholder="none"
            inputMode="decimal"
            className="w-32"
            aria-label="Daily budget in USD"
          />
        </label>
        <label className="grid gap-1 text-xs text-muted-foreground">
          Monthly (USD)
          <Input
            value={month}
            onChange={(e) => setMonth(e.target.value)}
            placeholder="none"
            inputMode="decimal"
            className="w-32"
            aria-label="Monthly budget in USD"
          />
        </label>
        <Button onClick={save}>Save budgets</Button>
        {saved && <span className="text-xs text-muted-foreground">Saved.</span>}
      </div>
    </div>
  )
}
