import { useCallback, useEffect, useState } from 'react'
import {
  getSettings,
  patchBudget,
  patchSettings,
  patchSettingValues,
  usageBudget,
} from '../../api/client'
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
      {values && <AgentCard values={values} onError={setError} onSaved={refresh} />}
      <BudgetsCard />
    </div>
  )
}

// AgentCard edits the typed runtime settings that used to be env vars:
// the context-budget fallback and the skill-pack allowlist. Empty
// resets a key to its built-in default; changes apply within seconds,
// no restart.
function AgentCard({
  values,
  onError,
  onSaved,
}: {
  values: Record<string, string>
  onError: (msg: string) => void
  onSaved: () => void
}) {
  const [budget, setBudget] = useState(values.session_token_budget ?? '')
  const [allowlist, setAllowlist] = useState(values.skills_allowlist ?? '')
  const [gitName, setGitName] = useState(values.git_author_name ?? '')
  const [gitEmail, setGitEmail] = useState(values.git_author_email ?? '')
  const [saved, setSaved] = useState(false)

  const save = () => {
    setSaved(false)
    patchSettingValues({
      session_token_budget: budget.trim(),
      skills_allowlist: allowlist.trim(),
      git_author_name: gitName.trim(),
      git_author_email: gitEmail.trim(),
    })
      .then(() => {
        setSaved(true)
        onSaved()
      })
      .catch((err: unknown) => onError(errText(err)))
  }

  return (
    <div className="rounded-xl border border-border p-4">
      <div className="text-sm font-medium">Agent</div>
      <p className="mt-0.5 text-xs text-muted-foreground">
        Runtime knobs, applied within seconds — no restart.
      </p>
      <div className="mt-3 flex flex-wrap items-end gap-3">
        <label className="grid gap-1 text-xs text-muted-foreground">
          Context budget (tokens)
          <Input
            value={budget}
            onChange={(e) => setBudget(e.target.value)}
            placeholder="60000"
            inputMode="numeric"
            className="w-36"
            aria-label="Session token budget"
          />
        </label>
        <label className="grid gap-1 text-xs text-muted-foreground">
          Skill allowlist (comma-separated)
          <Input
            value={allowlist}
            onChange={(e) => setAllowlist(e.target.value)}
            placeholder="empty = all packs"
            className="w-72"
            aria-label="Skills allowlist"
          />
        </label>
        <label className="grid gap-1 text-xs text-muted-foreground">
          Git author name
          <Input
            value={gitName}
            onChange={(e) => setGitName(e.target.value)}
            placeholder="Your Name"
            className="w-48"
            aria-label="Git author name"
          />
        </label>
        <label className="grid gap-1 text-xs text-muted-foreground">
          Git author email
          <Input
            value={gitEmail}
            onChange={(e) => setGitEmail(e.target.value)}
            placeholder="you@example.com"
            className="w-56"
            aria-label="Git author email"
          />
        </label>
        <Button onClick={save}>Save</Button>
        {saved && <span className="text-xs text-muted-foreground">Saved.</span>}
      </div>
      <p className="mt-2 text-xs text-muted-foreground">
        Budget caps the projected context when the serving model&apos;s window is unknown (a
        known window still wins at 60%). Allowlist restricts which skill packs the agent may
        load; empty allows all.
      </p>
      <p className="mt-2 text-xs text-muted-foreground">
        Git author name/email are used for mission commits; set them to your GitHub identity so
        pushed commits link to your account.
      </p>
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
