import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { createMission, createSchedule, patchSchedule } from '../../api/client'
import type { AdminAgent, Schedule } from '../../api/types'
import { useAgents } from '../AgentPicker'
import { slugify } from '../settings/AgentForm'
import { cronPresets, type CronPresetValue, presetFor } from '../../lib/schedules'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Label } from '../ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Textarea } from '../ui/textarea'
import { errText } from '../settings/util'

const kinds = [
  {
    value: 'research',
    label: 'Research',
    description: 'Investigate, gather sources, write findings.',
  },
  {
    value: 'coding',
    label: 'Coding',
    description: 'Work a repository: plan, edit, verify.',
  },
] as const

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
  const [goal, setGoal] = useState('')
  const [kind, setKind] = useState<(typeof kinds)[number]['value']>('research')
  const [agentID, setAgentID] = useState('')
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [route, setRoute] = useState('')
  const [reviewRoute, setReviewRoute] = useState('')
  const [escalationRoute, setEscalationRoute] = useState('')
  const [budget, setBudget] = useState('')
  const [autoApproveSafe, setAutoApproveSafe] = useState(true)
  const [busy, setBusy] = useState(false)

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
    setAgentID(schedule.mission_template.agent_id ?? '')
    setAutoApproveSafe(schedule.mission_template.auto_approve_safe ?? true)
    setShowAdvanced(false)
    setRoute(schedule.mission_template.route ?? '')
    setReviewRoute(schedule.mission_template.review_route ?? '')
    setMaxIterations(
      schedule.mission_template.max_iterations != null
        ? String(schedule.mission_template.max_iterations)
        : '',
    )
    setBudget(
      schedule.mission_template.budget_usd != null ? String(schedule.mission_template.budget_usd) : '',
    )
    setExpiresAt(schedule.expires_at ? schedule.expires_at.slice(0, 16) : '')
    setCronError(null)
  }, [mode, schedule])

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
      budget_usd: budget ? Number(budget) : undefined,
      auto_approve_safe: autoApproveSafe,
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
        budget_usd: budget ? Number(budget) : undefined,
        auto_approve_safe: autoApproveSafe,
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
        budget_usd: budget ? Number(budget) : undefined,
        auto_approve_safe: autoApproveSafe,
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
    <div className="space-y-8 pb-24">
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
          onChange={(e) => setGoal(e.target.value)}
          placeholder="What should this mission accomplish?"
          rows={5}
          autoFocus
          className="text-base"
        />

        <div className="grid gap-3 sm:grid-cols-2">
          {kinds.map((k) => {
            const disabled = repeat && k.value === 'coding'
            const selected = kind === k.value
            return (
              <button
                key={k.value}
                type="button"
                disabled={disabled}
                onClick={() => setKind(k.value)}
                aria-pressed={selected}
                className={`rounded-lg border p-3 text-left transition disabled:cursor-not-allowed disabled:opacity-50 ${
                  selected ? 'border-primary bg-accent' : 'border-border hover:bg-muted'
                }`}
              >
                <span className="text-sm font-medium">{k.label}</span>
                <p className="mt-0.5 text-xs text-muted-foreground">{k.description}</p>
              </button>
            )
          })}
        </div>
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
                if (kind === 'coding') setKind('research')
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
              <Label htmlFor="mission-max-iterations">Max iterations</Label>
              <Input
                id="mission-max-iterations"
                type="number"
                value={maxIterations}
                onChange={(e) => setMaxIterations(e.target.value)}
                placeholder="Default"
              />
            </div>

            <div className="space-y-1.5">
              <Label htmlFor="mission-expires">Expires</Label>
              <Input
                id="mission-expires"
                type="datetime-local"
                value={expiresAt}
                onChange={(e) => setExpiresAt(e.target.value)}
              />
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

        <button
          type="button"
          onClick={() => setShowAdvanced((v) => !v)}
          className="text-xs text-muted-foreground underline underline-offset-2 hover:text-foreground"
        >
          {showAdvanced ? 'Hide advanced options' : 'Show advanced options'}
        </button>

        {showAdvanced && (
          <div className="grid gap-4 rounded-lg border border-border p-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="mission-route">Route</Label>
              <Input
                id="mission-route"
                value={route}
                onChange={(e) => setRoute(e.target.value)}
                placeholder="default"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="mission-review-route">Review route</Label>
              <Input
                id="mission-review-route"
                value={reviewRoute}
                onChange={(e) => setReviewRoute(e.target.value)}
                placeholder="default"
              />
            </div>
            {mode === 'create' && !repeat && (
              <div className="space-y-1.5">
                <Label htmlFor="mission-escalation-route">Escalation route</Label>
                <Input
                  id="mission-escalation-route"
                  value={escalationRoute}
                  onChange={(e) => setEscalationRoute(e.target.value)}
                  placeholder="Off, set to switch route after a failed or reworked turn"
                />
              </div>
            )}
            <div className="space-y-1.5">
              <Label htmlFor="mission-budget">Budget (USD)</Label>
              <Input
                id="mission-budget"
                type="number"
                value={budget}
                onChange={(e) => setBudget(e.target.value)}
                placeholder="No limit"
              />
            </div>
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
                  Runs unattended without pausing for approval on routine commands. Destructive or
                  unrecognized commands still always ask.
                </span>
              </span>
            </label>
          </div>
        )}
      </section>

      <div className="fixed inset-x-0 bottom-0 border-t border-border bg-background/95 backdrop-blur">
        <div className="mx-auto flex max-w-3xl justify-end gap-2 px-4 py-3">
          <Button variant="outline" disabled={busy} onClick={onCancel}>
            Cancel
          </Button>
          <Button disabled={!canSubmit || busy} onClick={() => void submit()}>
            {submitLabel}
          </Button>
        </div>
      </div>
    </div>
  )
}
