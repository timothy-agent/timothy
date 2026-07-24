import { useState } from 'react'
import { toast } from 'sonner'
import { createMission } from '../../api/client'
import type { AdminAgent } from '../../api/types'
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
import { errText } from '../settings/util'

const kinds = [
  { value: 'research', label: 'Research' },
  { value: 'coding', label: 'Coding' },
  { value: 'scheduled', label: 'Scheduled' },
] as const

export function NewMissionDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (id: string) => void
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

  const pickAgent = (id: string) => {
    setAgentID(id)
    const agent = agents.find((a) => a.id === id)
    if (agent) {
      setReviewRoute(agent.review_route ?? '')
      if (agent.budget_usd != null) setBudget(String(agent.budget_usd))
    }
  }

  const canSubmit = goal.trim() !== '' && (kind !== 'coding' || repoPath.trim() !== '')

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
  }

  const submit = async () => {
    setBusy(true)
    try {
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
      reset()
      onOpenChange(false)
      onCreated(id)
    } catch (err) {
      toast.error('Could not create mission', { description: errText(err) })
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
                  <SelectItem key={k.value} value={k.value}>
                    {k.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {kind === 'coding' && (
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
              <div className="space-y-1.5">
                <Label htmlFor="mission-escalation-route">Escalation route</Label>
                <Input
                  id="mission-escalation-route"
                  value={escalationRoute}
                  onChange={(e) => setEscalationRoute(e.target.value)}
                  placeholder="Off — set to switch route after a failed or reworked turn"
                />
              </div>
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
            Create mission
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
