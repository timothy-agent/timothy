import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import {
  classifyMission,
  createMission,
  createSchedule,
  type ExecutorOption,
  getMissionExecutorOptions,
  getSettings,
  patchSchedule,
} from '../../api/client'
import type { AdminAgent, Schedule } from '../../api/types'
import { useAgents, useRoutes } from '../AgentPicker'
import { slugify } from '../settings/AgentForm'
import { cronPresets, type CronPresetValue, presetFor } from '../../lib/schedules'
import { CURRENCIES } from '../../lib/currencies'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Calendar } from '../ui/calendar'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '../ui/collapsible'
import { Input } from '../ui/input'
import { Label } from '../ui/label'
import { Popover, PopoverContent, PopoverTrigger } from '../ui/popover'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Textarea } from '../ui/textarea'
import { errText } from '../settings/util'

type Kind = 'coding' | 'general'

const kindCopy: Record<Kind, string> = {
  coding: 'Coding · branches from repo',
  general: 'General · scratch workspace',
}

// A schedule's mission_template carries route/review_route/budget as
// explicit undefined when unset (see types.ts) — "non-default" means
// any of them, or a non-default agent, actually has a value.
function hasNonDefaults(t: Schedule['mission_template']): boolean {
  return !!(
    t.agent_id ||
    t.route ||
    t.review_route ||
    t.budget_amount != null ||
    t.harness ||
    t.environment
  )
}

// Radix Select.Item rejects an empty string value, so the "no route
// chosen" state is represented by this sentinel on the wire between the
// Select and the route/reviewRoute/escalationRoute state (which stay ''
// to match the API's own empty-means-default semantics).
const ROUTE_DEFAULT = '__default__'

// Sentinel for the executor Select's "apply the settings default"
// choice — wire value stays '' (omit harness from the create payload)
// to match the API's own empty-means-default semantics.
const EXECUTOR_DEFAULT = '__default__'

// executorChoices maps a harness Select value to its label — easy to
// extend as more harnesses register; claude-cli is the only one today.
const executorChoices: { value: string; label: string }[] = [
  { value: EXECUTOR_DEFAULT, label: 'Default (from settings)' },
  { value: 'native', label: 'Native' },
  { value: 'claude-cli', label: 'Claude Code' },
]

// Sentinel for the environment Select's "auto-detect" choice — wire
// value stays '' (omit environment from the create payload) to match
// the API's own empty-means-auto-detect semantics (D-05x).
const ENVIRONMENT_AUTO = '__auto__'

// environmentChoices maps an environment Select value to its label —
// mirrors sandboxd's image allowlist (internal/sandboxd/manager.go).
const environmentChoices: { value: string; label: string }[] = [
  { value: ENVIRONMENT_AUTO, label: 'Auto-detect' },
  { value: 'base', label: 'Base' },
  { value: 'go', label: 'Go' },
  { value: 'node', label: 'Node' },
  { value: 'python', label: 'Python' },
  { value: 'java', label: 'Java' },
  { value: 'php', label: 'PHP' },
]

// expiresAt is stored as the wire-compatible 'YYYY-MM-DDTHH:mm' string the
// API already expects; these split it into a Date (for the calendar) and a
// 'HH:mm' string (for the time input) and back.
function expiresAtToDate(v: string): Date | undefined {
  if (!v) return undefined
  const [datePart, timePart] = v.split('T')
  const [y, m, d] = datePart.split('-').map(Number)
  const [h, min] = (timePart ?? '00:00').split(':').map(Number)
  return new Date(y, m - 1, d, h, min)
}

function expiresAtToTime(v: string): string {
  return v.split('T')[1] ?? '00:00'
}

function dateAndTimeToExpiresAt(date: Date, time: string): string {
  const y = date.getFullYear()
  const m = String(date.getMonth() + 1).padStart(2, '0')
  const d = String(date.getDate()).padStart(2, '0')
  return `${y}-${m}-${d}T${time}`
}

function formatExpiresAt(v: string): string {
  const date = expiresAtToDate(v)
  if (!date) return 'Never'
  return `${date.toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  })}, ${expiresAtToTime(v)}`
}

// MissionForm is the shared form behind both the new-mission page and
// the edit-schedule page. In 'create' mode it submits a one-off
// mission or, with "Repeat on schedule" on, a new schedule. In 'edit'
// mode it always patches the given schedule and locks out run-once,
// coding, and the escalation route — the same constraints
// NewMissionDialog/ScheduleDialog enforced.
export function MissionForm({
  mode,
  schedule,
  onDone,
  onCancel,
}: {
  mode: 'create' | 'edit'
  schedule?: Schedule
  onDone: (result: { kind: 'mission' | 'schedule'; id: string }) => void
  onCancel: () => void
}) {
  const agents = useAgents()
  const routes = useRoutes()
  const enabledRoutes = routes?.filter((r) => r.enabled) ?? []
  const [goal, setGoal] = useState('')
  const [kind, setKind] = useState<Kind>('general')
  // kindLocked freezes kind against further auto-classify calls once
  // the user has explicitly chosen it (chip click, or the repeat-mode
  // general override below) — cleared when the goal is emptied, which
  // resets to auto-detect for whatever's typed next.
  const [kindLocked, setKindLocked] = useState(false)
  const [classifying, setClassifying] = useState(false)
  const [agentID, setAgentID] = useState('')
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [route, setRoute] = useState('')
  const [reviewRoute, setReviewRoute] = useState('')
  const [escalationRoute, setEscalationRoute] = useState('')
  const [budget, setBudget] = useState('')
  const [budgetCurrency, setBudgetCurrency] = useState('USD')
  const [autoApproveSafe, setAutoApproveSafe] = useState(true)
  const [harness, setHarness] = useState('')
  const [environment, setEnvironment] = useState('')
  const [executorOptions, setExecutorOptions] = useState<ExecutorOption[] | null>(null)
  const [busy, setBusy] = useState(false)

  // Live executor pairing/usability preview: coding-only, refetched
  // whenever the kind flips to coding or the route selection changes.
  // Best-effort — a failed fetch degrades to a plain, fully-enabled
  // select with no live info, the server validates on submit anyway.
  useEffect(() => {
    if (kind !== 'coding') {
      setExecutorOptions(null)
      return
    }
    getMissionExecutorOptions(route || undefined)
      .then(setExecutorOptions)
      .catch(() => setExecutorOptions(null))
  }, [kind, route])

  // Pre-select the settings page's configured default currency for a
  // fresh create — edit mode below overwrites this with the schedule's
  // own saved currency once it loads.
  useEffect(() => {
    if (mode !== 'create') return
    getSettings()
      .then((s) => {
        const v = s.values.default_currency
        if (v) setBudgetCurrency(v)
      })
      .catch(() => {
        // Best-effort: falls back to the USD default already set.
      })
  }, [mode])

  // Repeat-on-schedule fields — read/submitted whenever repeat is on
  // (create mode) or always (edit mode, which only ever edits a
  // schedule).
  const [repeat, setRepeat] = useState(mode === 'edit')
  const [scheduleName, setScheduleName] = useState('')
  const [preset, setPreset] = useState<CronPresetValue>('daily-7am')
  const [cron, setCron] = useState<string>(cronPresets[0].cron ?? '0 7 * * *')
  const [cronError, setCronError] = useState<string | null>(null)
  const [maxIterations, setMaxIterations] = useState('')
  const [expiresAt, setExpiresAt] = useState('')

  // Edit mode: re-seed every time the schedule to edit changes.
  useEffect(() => {
    if (mode !== 'edit' || !schedule) return
    setScheduleName(schedule.name)
    setCron(schedule.cron)
    setPreset(presetFor(schedule.cron))
    setGoal(schedule.mission_template.goal)
    setKind(schedule.mission_template.kind)
    setKindLocked(true)
    setAgentID(schedule.mission_template.agent_id ?? '')
    setAutoApproveSafe(schedule.mission_template.auto_approve_safe ?? true)
    setShowAdvanced(hasNonDefaults(schedule.mission_template))
    setRoute(schedule.mission_template.route ?? '')
    setReviewRoute(schedule.mission_template.review_route ?? '')
    setMaxIterations(
      schedule.mission_template.max_iterations != null
        ? String(schedule.mission_template.max_iterations)
        : '',
    )
    setBudget(
      schedule.mission_template.budget_amount != null
        ? String(schedule.mission_template.budget_amount)
        : '',
    )
    setBudgetCurrency(schedule.mission_template.budget_currency || 'USD')
    setHarness(schedule.mission_template.harness ?? '')
    setEnvironment(schedule.mission_template.environment ?? '')
    setExpiresAt(schedule.expires_at ? schedule.expires_at.slice(0, 16) : '')
    setCronError(null)
  }, [mode, schedule])

  // Live kind inference: debounced 600ms after goal edits, skipped
  // once the user has locked a manual choice or the goal is empty.
  // Unlocking on an emptied goal happens in the textarea's onChange
  // below (a direct user edit), not here — this effect also runs on
  // mount/schedule-load with the PREVIOUS render's goal, and clearing
  // the lock from here would race the schedule-seed effect's own
  // setKindLocked(true) and stomp it back to false.
  useEffect(() => {
    if (goal.trim() === '' || kindLocked) return
    setClassifying(true)
    const t = setTimeout(() => {
      classifyMission(goal.trim())
        .then((r) => setKind(r.kind))
        .catch(() => {
          // Best-effort preview: a failed classify leaves whatever kind
          // was already showing rather than blocking the form.
        })
        .finally(() => setClassifying(false))
    }, 600)
    return () => {
      clearTimeout(t)
      setClassifying(false)
    }
  }, [goal, kindLocked])

  const onGoalChange = (v: string) => {
    setGoal(v)
    if (v.trim() === '') setKindLocked(false)
  }

  const toggleKind = () => {
    if (repeat && kind === 'general') return // coding is unavailable while repeating
    setKind((k) => (k === 'coding' ? 'general' : 'coding'))
    setKindLocked(true)
  }

  const pickAgent = (id: string) => {
    setAgentID(id)
    const agent = agents.find((a) => a.id === id)
    if (agent) {
      setReviewRoute(agent.review_route ?? '')
      if (agent.budget_usd != null) setBudget(String(agent.budget_usd))
    }
  }

  const pickPreset = (v: CronPresetValue) => {
    setPreset(v)
    const found = cronPresets.find((p) => p.value === v)
    if (found?.cron) setCron(found.cron)
  }

  const pickExpiresDate = (date: Date | undefined) => {
    if (!date) return
    setExpiresAt(dateAndTimeToExpiresAt(date, expiresAtToTime(expiresAt) || '00:00'))
  }

  const pickExpiresTime = (time: string) => {
    const date = expiresAtToDate(expiresAt) ?? new Date()
    setExpiresAt(dateAndTimeToExpiresAt(date, time))
  }

  // A client-side 5-field shape check only — the server is the
  // authoritative cron validator (robfig/cron), this just catches
  // obvious typos before a round trip.
  const validCronShape = (v: string) => v.trim().split(/\s+/).length === 5

  const onCronChange = (v: string) => {
    setCron(v)
    setCronError(null)
  }

  const canSubmit =
    mode === 'edit'
      ? scheduleName.trim() !== '' && goal.trim() !== '' && validCronShape(cron)
      : goal.trim() !== '' && (!repeat || validCronShape(cron))

  const submitMission = async () => {
    const { id } = await createMission({
      goal: goal.trim(),
      kind,
      agent_id: agentID || undefined,
      route: route || undefined,
      review_route: reviewRoute || undefined,
      escalation_route: escalationRoute || undefined,
      budget_amount: budget ? Number(budget) : undefined,
      budget_currency: budget ? budgetCurrency : undefined,
      auto_approve_safe: autoApproveSafe,
      harness: kind === 'coding' ? harness || undefined : undefined,
      environment: kind === 'coding' ? environment || undefined : undefined,
    })
    toast.success('Mission created')
    onDone({ kind: 'mission', id })
  }

  const submitSchedule = async () => {
    const { id } = await createSchedule({
      name: slugify(scheduleName || goal),
      cron,
      mission_template: {
        goal: goal.trim(),
        kind,
        agent_id: agentID || undefined,
        route: route || undefined,
        review_route: reviewRoute || undefined,
        max_iterations: maxIterations ? Number(maxIterations) : undefined,
        budget_amount: budget ? Number(budget) : undefined,
        budget_currency: budget ? budgetCurrency : undefined,
        auto_approve_safe: autoApproveSafe,
        harness: kind === 'coding' ? harness || undefined : undefined,
        environment: kind === 'coding' ? environment || undefined : undefined,
      },
      expires_at: expiresAt ? new Date(expiresAt).toISOString() : undefined,
    })
    toast.success('Schedule created')
    onDone({ kind: 'schedule', id })
  }

  const submitEdit = async () => {
    if (!schedule) return
    const sc = await patchSchedule(schedule.id, {
      name: slugify(scheduleName),
      cron,
      mission_template: {
        goal: goal.trim(),
        kind: schedule.mission_template.kind,
        agent_id: agentID || undefined,
        route: route || undefined,
        review_route: reviewRoute || undefined,
        max_iterations: maxIterations ? Number(maxIterations) : undefined,
        budget_amount: budget ? Number(budget) : undefined,
        budget_currency: budget ? budgetCurrency : undefined,
        auto_approve_safe: autoApproveSafe,
        harness: schedule.mission_template.kind === 'coding' ? harness || undefined : undefined,
        environment:
          schedule.mission_template.kind === 'coding' ? environment || undefined : undefined,
      },
      expires_at: expiresAt ? new Date(expiresAt).toISOString() : null,
    })
    toast.success('Schedule updated')
    onDone({ kind: 'schedule', id: sc.id })
  }

  const submit = async () => {
    if (repeat && !validCronShape(cron)) {
      setCronError('Cron must have 5 space-separated fields (minute hour day month weekday).')
      return
    }
    setBusy(true)
    try {
      if (mode === 'edit') {
        await submitEdit()
      } else if (repeat) {
        await submitSchedule()
      } else {
        await submitMission()
      }
    } catch (err) {
      const label = mode === 'edit' ? 'update' : repeat ? 'create' : 'create'
      const noun = mode === 'edit' || repeat ? 'schedule' : 'mission'
      toast.error(`Could not ${label} ${noun}`, { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  const submitLabel = mode === 'edit' ? 'Save schedule' : repeat ? 'Create schedule' : 'Create mission'

  return (
    <div className="space-y-8">
      <section className="space-y-3">
        <div>
          <h2 className="text-sm font-semibold">Goal</h2>
          <p className="text-sm text-muted-foreground">
            What should this mission accomplish{repeat ? ' each time it fires' : ''}?
          </p>
        </div>
        <Textarea
          id="mission-goal"
          aria-label="Goal"
          value={goal}
          onChange={(e) => onGoalChange(e.target.value)}
          placeholder="What should this mission accomplish?"
          rows={10}
          autoFocus
          className="min-h-60 text-base"
        />

        {goal.trim() !== '' && (
          <button type="button" onClick={toggleKind} disabled={repeat && kind === 'general'}>
            <Badge variant={classifying ? 'outline' : 'secondary'} className="cursor-pointer">
              {classifying ? 'Detecting…' : kindCopy[kind]}
            </Badge>
          </button>
        )}
        {repeat && (
          <p className="text-xs text-muted-foreground">
            Coding missions aren't supported on a recurring schedule yet: each fire has no
            repository to work in.
          </p>
        )}
      </section>

      <section className="space-y-3">
        <div>
          <h2 className="text-sm font-semibold">When it runs</h2>
          <p className="text-sm text-muted-foreground">
            {mode === 'edit'
              ? 'This schedule fires the mission above on the cron below.'
              : 'Run it now, or fire it on a recurring schedule instead.'}
          </p>
        </div>

        {mode === 'create' && (
          <div className="inline-flex rounded-lg bg-muted p-1 text-sm">
            <button
              type="button"
              onClick={() => setRepeat(false)}
              aria-pressed={!repeat}
              className={`rounded-md px-3 py-1.5 font-medium transition ${
                !repeat ? 'bg-background shadow-sm' : 'text-muted-foreground'
              }`}
            >
              Run once
            </button>
            <button
              type="button"
              onClick={() => {
                setRepeat(true)
                if (kind === 'coding') {
                  setKind('general')
                  setKindLocked(true)
                }
              }}
              aria-pressed={repeat}
              className={`rounded-md px-3 py-1.5 font-medium transition ${
                repeat ? 'bg-background shadow-sm' : 'text-muted-foreground'
              }`}
            >
              Repeat on schedule
            </button>
          </div>
        )}

        {repeat && (
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="mission-schedule-name">Schedule name</Label>
              <Input
                id="mission-schedule-name"
                value={scheduleName}
                onChange={(e) => setScheduleName(e.target.value)}
                placeholder={slugify(goal) || 'schedule name'}
              />
            </div>

            <div className="space-y-1.5">
              <Label>Runs</Label>
              <Select value={preset} onValueChange={(v) => pickPreset(v as CronPresetValue)}>
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {cronPresets.map((p) => (
                    <SelectItem key={p.value} value={p.value}>
                      {p.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {preset === 'custom' && (
                <Input
                  aria-label="Cron expression"
                  value={cron}
                  onChange={(e) => onCronChange(e.target.value)}
                  placeholder="0 7 * * *"
                  className="font-mono"
                />
              )}
              {cronError && <p className="text-xs text-destructive">{cronError}</p>}
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="mission-expires">Expires</Label>
              <Popover>
                <PopoverTrigger asChild>
                  <Button id="mission-expires" variant="outline" className="w-full justify-start">
                    {formatExpiresAt(expiresAt)}
                  </Button>
                </PopoverTrigger>
                <PopoverContent className="w-auto p-0">
                  <Calendar
                    mode="single"
                    selected={expiresAtToDate(expiresAt)}
                    onSelect={pickExpiresDate}
                  />
                  <div className="flex items-center gap-2 border-t border-border p-2.5">
                    <Input
                      aria-label="Expires time"
                      type="time"
                      value={expiresAtToTime(expiresAt)}
                      onChange={(e) => pickExpiresTime(e.target.value)}
                      disabled={!expiresAt}
                      className="flex-1"
                    />
                    <Button variant="outline" size="sm" onClick={() => setExpiresAt('')}>
                      Clear
                    </Button>
                  </div>
                </PopoverContent>
              </Popover>
              <p className="text-xs text-muted-foreground">
                Server time. The schedule stops firing after this moment. Empty means it never
                expires.
              </p>
            </div>
          </div>
        )}
      </section>

      <section className="space-y-3">
        <div>
          <h2 className="text-sm font-semibold">Agent &amp; limits</h2>
          <p className="text-sm text-muted-foreground">Who runs it, and what it's allowed to spend.</p>
        </div>

        {agents.length > 0 && (
          <div className="space-y-1.5">
            <Label>Agent</Label>
            <Select value={agentID} onValueChange={pickAgent}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder="Default" />
              </SelectTrigger>
              <SelectContent>
                {agents.map((a: AdminAgent) => (
                  <SelectItem key={a.id} value={a.id}>
                    {a.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}

        {kind === 'coding' && (
          <div className="space-y-1.5">
            <Label htmlFor="mission-executor">Executor</Label>
            <Select
              value={harness || EXECUTOR_DEFAULT}
              onValueChange={(v) => setHarness(v === EXECUTOR_DEFAULT ? '' : v)}
            >
              <SelectTrigger id="mission-executor" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {executorChoices.map((c) => {
                  const opt = executorOptions?.find((o) => o.harness === c.value)
                  const disabled = !!opt && !opt.usable
                  return (
                    <SelectItem
                      key={c.value}
                      value={c.value}
                      disabled={disabled}
                      title={disabled ? opt?.reason : undefined}
                    >
                      {c.label}
                    </SelectItem>
                  )
                })}
              </SelectContent>
            </Select>
            {(() => {
              // executorOptions is keyed by real registered harness
              // names only — "Default"/"Native" never have a match,
              // so the preview only ever renders for a concretely
              // selected harness (e.g. "claude-cli").
              const selected = executorOptions?.find((o) => o.harness === harness)
              if (!selected?.usable) return null
              return (
                <p className="text-xs text-muted-foreground">
                  runs via {selected.provider_name}/{selected.model}
                </p>
              )
            })()}
          </div>
        )}

        {kind === 'coding' && (
          <div className="space-y-1.5">
            <Label htmlFor="mission-environment">Environment</Label>
            <Select
              value={environment || ENVIRONMENT_AUTO}
              onValueChange={(v) => setEnvironment(v === ENVIRONMENT_AUTO ? '' : v)}
            >
              <SelectTrigger id="mission-environment" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {environmentChoices.map((c) => (
                  <SelectItem key={c.value} value={c.value}>
                    {c.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}

        <Collapsible open={showAdvanced} onOpenChange={setShowAdvanced}>
          <CollapsibleTrigger className="text-xs text-muted-foreground underline underline-offset-2 hover:text-foreground">
            {showAdvanced ? 'Hide advanced options' : 'Show advanced options'}
          </CollapsibleTrigger>
          <CollapsibleContent>
            <div className="mt-3 grid gap-4 rounded-lg border border-border p-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label htmlFor="mission-route">Route</Label>
                {routes === null ? (
                  <Input
                    id="mission-route"
                    value={route}
                    onChange={(e) => setRoute(e.target.value)}
                    placeholder="default"
                  />
                ) : (
                  <Select
                    value={route || ROUTE_DEFAULT}
                    onValueChange={(v) => setRoute(v === ROUTE_DEFAULT ? '' : v)}
                  >
                    <SelectTrigger id="mission-route" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={ROUTE_DEFAULT}>Default</SelectItem>
                      {enabledRoutes.map((r) => (
                        <SelectItem key={r.name} value={r.name}>
                          {r.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="mission-review-route">Review route</Label>
                {routes === null ? (
                  <Input
                    id="mission-review-route"
                    value={reviewRoute}
                    onChange={(e) => setReviewRoute(e.target.value)}
                    placeholder="default"
                  />
                ) : (
                  <Select
                    value={reviewRoute || ROUTE_DEFAULT}
                    onValueChange={(v) => setReviewRoute(v === ROUTE_DEFAULT ? '' : v)}
                  >
                    <SelectTrigger id="mission-review-route" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={ROUTE_DEFAULT}>Default</SelectItem>
                      {enabledRoutes.map((r) => (
                        <SelectItem key={r.name} value={r.name}>
                          {r.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </div>
              {mode === 'create' && !repeat && (
                <div className="space-y-1.5">
                  <Label htmlFor="mission-escalation-route">Escalation route</Label>
                  {routes === null ? (
                    <Input
                      id="mission-escalation-route"
                      value={escalationRoute}
                      onChange={(e) => setEscalationRoute(e.target.value)}
                      placeholder="Off, set to switch route after a failed or reworked turn"
                    />
                  ) : (
                    <Select
                      value={escalationRoute || ROUTE_DEFAULT}
                      onValueChange={(v) => setEscalationRoute(v === ROUTE_DEFAULT ? '' : v)}
                    >
                      <SelectTrigger id="mission-escalation-route" className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value={ROUTE_DEFAULT}>Off</SelectItem>
                        {enabledRoutes.map((r) => (
                          <SelectItem key={r.name} value={r.name}>
                            {r.name}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  )}
                </div>
              )}
              <div className="space-y-1.5">
                <Label htmlFor="mission-budget">Budget</Label>
                <div className="flex gap-2">
                  <Input
                    id="mission-budget"
                    type="number"
                    value={budget}
                    onChange={(e) => setBudget(e.target.value)}
                    placeholder="No limit"
                    className="flex-1"
                  />
                  <Select value={budgetCurrency} onValueChange={setBudgetCurrency}>
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
              </div>
              {repeat && (
                <div className="space-y-1.5">
                  <Label htmlFor="mission-max-iterations">Max iterations</Label>
                  <Input
                    id="mission-max-iterations"
                    type="number"
                    value={maxIterations}
                    onChange={(e) => setMaxIterations(e.target.value)}
                    placeholder="Default"
                  />
                </div>
              )}
              <label
                htmlFor="mission-auto-approve"
                className="flex items-start gap-2 text-sm sm:col-span-2"
              >
                <input
                  id="mission-auto-approve"
                  type="checkbox"
                  checked={autoApproveSafe}
                  onChange={(e) => setAutoApproveSafe(e.target.checked)}
                  className="mt-0.5"
                />
                <span>
                  Auto-approve safe tool calls
                  <span className="block text-xs text-muted-foreground">
                    Runs unattended without pausing for approval on routine commands. Destructive
                    or unrecognized commands still always ask.
                  </span>
                </span>
              </label>
            </div>
          </CollapsibleContent>
        </Collapsible>
      </section>

      <div className="flex gap-2">
        <Button variant="outline" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
        <Button disabled={!canSubmit || busy} onClick={() => void submit()}>
          {submitLabel}
        </Button>
      </div>
    </div>
  )
}
