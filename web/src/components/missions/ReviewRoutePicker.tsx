import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { getMissionExecutionPlan, patchMissionRouting } from '../../api/client'
import type { ExecutionPlanEntry, Mission } from '../../api/types'
import { useRoutes } from '../AgentPicker'
import { errText } from '../settings/util'
import { Button } from '../ui/button'
import { Input } from '../ui/input'
import { Label } from '../ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { unusableReason } from './executionPlan'
import { ModelPinSelect } from './MissionExecutionPlan'

interface ReviewRoutePickerProps {
  mission: Mission
  onSaved: () => void
}

// ReviewRoutePicker edits a paused mission's review route and model pin
// in place (D-100, issue #536): the same route select the create form
// renders, the prove phase's chain resolved through the execution plan
// endpoint so unusable entries stay listed but disabled with their
// reason, and a Save that calls PATCH .../routing. Worker and plan
// routes are not editable here.
export function ReviewRoutePicker({ mission, onSaved }: ReviewRoutePickerProps) {
  const routes = useRoutes()
  const enabledRoutes = routes?.filter((r) => r.enabled) ?? []
  const [reviewRoute, setReviewRoute] = useState(mission.review_route)
  const [reviewRouteModel, setReviewRouteModel] = useState(mission.review_route_model ?? '')
  const [entries, setEntries] = useState<ExecutionPlanEntry[] | null>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!reviewRoute) return
    let cancelled = false
    getMissionExecutionPlan({
      kind: mission.kind,
      route: mission.route,
      plan_route: mission.plan_route,
      review_route: reviewRoute,
      harness: mission.harness,
    }).then(
      (phases) => {
        if (!cancelled) setEntries(phases.find((p) => p.phase === 'prove')?.entries ?? [])
      },
      () => {
        if (!cancelled) setEntries([])
      },
    )
    return () => {
      cancelled = true
    }
  }, [reviewRoute, mission.kind, mission.route, mission.plan_route, mission.harness])

  const unusable = !!reviewRoute && entries !== null && entries.length > 0 && !entries.some((e) => e.usable)
  const dirty = reviewRoute !== mission.review_route || reviewRouteModel !== (mission.review_route_model ?? '')
  const selected = entries?.find((e) => (reviewRouteModel ? `${e.provider_name}/${e.model}` === reviewRouteModel : e.selected))

  const save = async () => {
    setSaving(true)
    try {
      await patchMissionRouting(mission.id, {
        review_route: reviewRoute,
        review_route_model: reviewRouteModel || undefined,
      })
      toast.success('Review route updated')
      onSaved()
    } catch (err) {
      toast.error('Could not change the review route', { description: errText(err) })
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-2 rounded-lg border border-border p-3">
      <Label htmlFor="mission-review-route">Review route</Label>
      <div className="flex flex-wrap items-center gap-2">
        {routes === null ? (
          <Input
            id="mission-review-route"
            className="w-48"
            value={reviewRoute}
            onChange={(e) => setReviewRoute(e.target.value)}
          />
        ) : (
          <Select value={reviewRoute} onValueChange={setReviewRoute}>
            <SelectTrigger id="mission-review-route" className="w-48">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {enabledRoutes.map((r) => (
                <SelectItem key={r.name} value={r.name}>
                  {r.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        {reviewRoute && entries && entries.length > 0 && (
          <ModelPinSelect
            label="Review model"
            entries={entries}
            pin={reviewRouteModel}
            onChange={setReviewRouteModel}
            selected={selected}
          />
        )}
        <Button size="sm" disabled={saving || !dirty || !reviewRoute || unusable} onClick={() => void save()}>
          Save
        </Button>
      </div>
      {unusable && entries && (
        <p role="alert" className="text-xs text-destructive">
          Route {reviewRoute} has no usable provider: {unusableReason(entries)}
        </p>
      )}
      <p className="text-xs text-muted-foreground">
        The next review round after resume runs on this route. Worker and plan routes stay as they are.
      </p>
    </div>
  )
}
