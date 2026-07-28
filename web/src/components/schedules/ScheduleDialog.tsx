import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { patchSchedule } from '../../api/client'
import type { AdminAgent, Schedule } from '../../api/types'
import { slugify } from '../settings/AgentForm'
import { errText } from '../settings/util'
import { useAgents } from '../AgentPicker'
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
import { cronPresets, type CronPresetValue, presetFor } from '../../lib/schedules'

// ScheduleDialog edits an existing recurring schedule — creation now
// happens from NewMissionDialog's "Repeat on schedule" toggle, so this
// dialog no longer has a create path or a kind field of its own: it
// preserves whatever kind the schedule's mission_template already
// carries.
export function ScheduleDialog({
  open,
  onOpenChange,
  schedule,
  onSaved,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  schedule?: Schedule
  onSaved: (sc: { id: string }) => void
}) {
  const agents = useAgents()

  const [name, setName] = useState('')
  const [cron, setCron] = useState('')
  const [preset, setPreset] = useState<CronPresetValue>('daily-7am')
  const [goal, setGoal] = useState('')
  const [agentID, setAgentID] = useState('')
  const [autoApproveSafe, setAutoApproveSafe] = useState(true)
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [route, setRoute] = useState('')
  const [reviewRoute, setReviewRoute] = useState('')
  const [maxIterations, setMaxIterations] = useState('')
  const [budget, setBudget] = useState('')
  const [expiresAt, setExpiresAt] = useState('')
  const [busy, setBusy] = useState(false)
  const [cronError, setCronError] = useState<string | null>(null)

  // Re-seed the form every time the dialog opens fresh from the
  // schedule being edited.
  useEffect(() => {
    if (!open || !schedule) return
    setName(schedule.name)
    setCron(schedule.cron)
    setPreset(presetFor(schedule.cron))
    setGoal(schedule.mission_template.goal)
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
  }, [open, schedule])

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

  const canSubmit = name.trim() !== '' && goal.trim() !== '' && validCronShape(cron)

  const submit = async () => {
    if (!schedule) return
    if (!validCronShape(cron)) {
      setCronError('Cron must have 5 space-separated fields (minute hour day month weekday).')
      return
    }
    setBusy(true)
    try {
      const missionTemplate = {
        goal: goal.trim(),
        kind: schedule.mission_template.kind,
        agent_id: agentID || undefined,
        route: route || undefined,
        review_route: reviewRoute || undefined,
        max_iterations: maxIterations ? Number(maxIterations) : undefined,
        budget_usd: budget ? Number(budget) : undefined,
        auto_approve_safe: autoApproveSafe,
      }
      const sc = await patchSchedule(schedule.id, {
        name: slugify(name),
        cron,
        mission_template: missionTemplate,
        expires_at: expiresAt ? new Date(expiresAt).toISOString() : null,
      })
      toast.success('Schedule updated')
      onOpenChange(false)
      onSaved({ id: sc.id })
    } catch (err) {
      toast.error('Could not update schedule', { description: errText(err) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Edit schedule</DialogTitle>
          <DialogDescription>
            A recurring mission that fires on the cron below.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="schedule-name">Name</Label>
            <Input
              id="schedule-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="daily-briefing"
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
            <Label htmlFor="schedule-goal">Goal</Label>
            <Textarea
              id="schedule-goal"
              value={goal}
              onChange={(e) => setGoal(e.target.value)}
              placeholder="What should this mission accomplish each time it fires?"
              rows={3}
            />
          </div>

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
                <Label htmlFor="schedule-route">Route</Label>
                <Input
                  id="schedule-route"
                  value={route}
                  onChange={(e) => setRoute(e.target.value)}
                  placeholder="default"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="schedule-review-route">Review route</Label>
                <Input
                  id="schedule-review-route"
                  value={reviewRoute}
                  onChange={(e) => setReviewRoute(e.target.value)}
                  placeholder="default"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="schedule-max-iterations">Max iterations</Label>
                <Input
                  id="schedule-max-iterations"
                  type="number"
                  value={maxIterations}
                  onChange={(e) => setMaxIterations(e.target.value)}
                  placeholder="Default"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="schedule-budget">Budget (USD)</Label>
                <Input
                  id="schedule-budget"
                  type="number"
                  value={budget}
                  onChange={(e) => setBudget(e.target.value)}
                  placeholder="No limit"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="schedule-expires">Expires</Label>
                <Input
                  id="schedule-expires"
                  type="datetime-local"
                  value={expiresAt}
                  onChange={(e) => setExpiresAt(e.target.value)}
                />
                <p className="text-xs text-muted-foreground">
                  Server time — the schedule stops firing after this moment. Empty means it never
                  expires.
                </p>
              </div>
              <label htmlFor="schedule-auto-approve" className="flex items-start gap-2 text-sm">
                <input
                  id="schedule-auto-approve"
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
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
