import { useState } from 'react'
import { toast } from 'sonner'
import { createMission, createSchedule } from '../../api/client'
import type { AdminAgent } from '../../api/types'
import { useAgents } from '../AgentPicker'
import { slugify } from '../settings/AgentForm'
import { cronPresets, type CronPresetValue } from '../../lib/schedules'
import { Button } from '../ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '../ui/dialog'
import { Input } from '../ui/input'
import { Label } from '../ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Textarea } from '../ui/textarea'
import { errText } from '../settings/util'

const kinds = [
  { value: 'research', label: 'Research' },
  { value: 'coding', label: 'Coding' },
] as const

export function NewMissionDialog({
  open,
  onOpenChange,
  onCreated,
  onScheduled,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (id: string) => void
  // Fires after a recurring schedule is created instead of a one-off
  // mission — the caller refreshes its schedule list; unlike onCreated
  // this never navigates, there is no mission yet to open.
  onScheduled?: () => void
}) {
  const agents = useAgents()
  const [goal, setGoal] = useState('')
  const [kind, setKind] = useState<(typeof kinds)[number]['value']>('research')
  const [agentID, setAgentID] = useState('')
  const [repoPath, setRepoPath] = useState('')
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [route, setRoute] = useState('')
  const [reviewRoute, setReviewRoute] = useState('')
  const [escalationRoute, setEscalationRoute] = useState('')
  const [budget, setBudget] = useState('')
  const [autoApproveSafe, setAutoApproveSafe] = useState(true)
  const [busy, setBusy] = useState(false)

  // Repeat-on-schedule fields — only read/submitted while repeat is on.
  const [repeat, setRepeat] = useState(false)
  const [scheduleName, setScheduleName] = useState('')
  const [preset, setPreset] = useState<CronPresetValue>('daily-7am')
  const [cron, setCron] = useState<string>(cronPresets[0].cron ?? '0 7 * * *')
  const [cronError, setCronError] = useState<string | null>(null)
  const [maxIterations, setMaxIterations] = useState('')
  const [expiresAt, setExpiresAt] = useState('')

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
    goal.trim() !== '' &&
    (kind !== 'coding' || repoPath.trim() !== '') &&
    (!repeat || validCronShape(cron))

  const reset = () => {
    setGoal('')
    setKind('research')
    setAgentID('')
    setRepoPath('')
    setShowAdvanced(false)
    setRoute('')
    setReviewRoute('')
    setEscalationRoute('')
    setBudget('')
    setAutoApproveSafe(true)
    setRepeat(false)
    setScheduleName('')
    setPreset('daily-7am')
    setCron(cronPresets[0].cron ?? '0 7 * * *')
    setCronError(null)
    setMaxIterations('')
    setExpiresAt('')
  }

  const submitMission = async () => {
    const { id } = await createMission({
      goal: goal.trim(),
      kind,
      agent_id: agentID || undefined,
      route: route || undefined,
      review_route: reviewRoute || undefined,
      escalation_route: escalationRoute || undefined,
      budget_usd: budget ? Number(budget) : undefined,
      repo_path: kind === 'coding' ? repoPath.trim() : undefined,
      auto_approve_safe: autoApproveSafe,
    })
    toast.success('Mission created')
    onCreated(id)
  }

  const submitSchedule = async () => {
    await createSchedule({
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
    onScheduled?.()
  }

  const submit = async () => {
    if (repeat && !validCronShape(cron)) {
      setCronError('Cron must have 5 space-separated fields (minute hour day month weekday).')
      return
    }
    setBusy(true)
    try {
      if (repeat) {
        await submitSchedule()
      } else {
        await submitMission()
      }
      reset()
      onOpenChange(false)
    } catch (err) {
      toast.error(repeat ? 'Could not create schedule' : 'Could not create mission', {
        description: errText(err),
      })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>New mission</DialogTitle>
          <DialogDescription>
            A long-running task that plans, executes, and reviews its own work.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="mission-goal">Goal</Label>
            <Textarea
              id="mission-goal"
              value={goal}
              onChange={(e) => setGoal(e.target.value)}
              placeholder="What should this mission accomplish?"
              rows={3}
            />
          </div>

          <div className="space-y-1.5">
            <Label>Kind</Label>
            <Select value={kind} onValueChange={(v) => setKind(v as typeof kind)}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {kinds.map((k) => (
                  <SelectItem key={k.value} value={k.value} disabled={repeat && k.value === 'coding'}>
                    {k.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {repeat && (
              <p className="text-xs text-muted-foreground">
                Coding missions aren't supported on a recurring schedule yet — each fire has no
                repository to work in.
              </p>
            )}
          </div>

          {kind === 'coding' && !repeat && (
            <div className="space-y-1.5">
              <Label htmlFor="mission-repo">Repository path</Label>
              <Input
                id="mission-repo"
                value={repoPath}
                onChange={(e) => setRepoPath(e.target.value)}
                placeholder="/workspace/my-repo"
              />
            </div>
          )}

          {agents.length > 0 && (
            <div className="space-y-1.5">
              <Label>Agent</Label>
              <Select value={agentID} onValueChange={pickAgent}>
                <SelectTrigger>
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

          <label htmlFor="mission-repeat" className="flex items-start gap-2 text-sm">
            <input
              id="mission-repeat"
              type="checkbox"
              checked={repeat}
              onChange={(e) => {
                setRepeat(e.target.checked)
                if (e.target.checked && kind === 'coding') setKind('research')
              }}
              className="mt-0.5"
            />
            <span>
              Repeat on schedule
              <span className="block text-xs text-muted-foreground">
                Fires this mission on a cron schedule instead of running it once.
              </span>
            </span>
          </label>

          {repeat && (
            <div className="space-y-3 rounded-lg border border-border p-3">
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
                  <SelectTrigger>
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
                  Server time — the schedule stops firing after this moment. Empty means it never
                  expires.
                </p>
              </div>
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
            <div className="space-y-3 rounded-lg border border-border p-3">
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
              {!repeat && (
                <div className="space-y-1.5">
                  <Label htmlFor="mission-escalation-route">Escalation route</Label>
                  <Input
                    id="mission-escalation-route"
                    value={escalationRoute}
                    onChange={(e) => setEscalationRoute(e.target.value)}
                    placeholder="Off — set to switch route after a failed or reworked turn"
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
              <label htmlFor="mission-auto-approve" className="flex items-start gap-2 text-sm">
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
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button disabled={!canSubmit || busy} onClick={() => void submit()}>
            {repeat ? 'Create schedule' : 'Create mission'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
