import { useState } from 'react'
import type { PlanAssumption, PlanUnit } from '../../api/types'
import { Button } from '../ui/button'
import { MarkdownField } from './MarkdownField'
import { PlanSection } from './PlanSection'

// answeredCopy mirrors PermissionBanner's own status-line pattern: the
// mission row's phase/pause_reason only clears once the harness has
// actually acted on the decision, so the card must not look
// unanswered for that span.
const answeredCopy: Record<'approve' | 'replan' | 'rediscover', string> = {
  approve: 'Approved, moving to generate…',
  replan: 'Replan requested…',
  rediscover: 'Sending back to discover…',
}

// PlanApprovalBanner renders only when a mission is parked on
// phase=plan with pause_reason=approval (auto_approve_plan: false).
// Same architecture as PermissionBanner: callbacks as props for
// isolated testing, and once answered the buttons are replaced by a
// status line so a second click can't fire before the mission row's
// own refetch catches up.
export function PlanApprovalBanner({
  units,
  assumptions,
  answeredDecision,
  onApprove,
  onReplan,
  onRediscover,
}: {
  units: PlanUnit[]
  assumptions?: PlanAssumption[]
  answeredDecision?: 'approve' | 'replan' | 'rediscover'
  onApprove: () => void
  onReplan: (feedback: string) => void
  onRediscover: () => void
}) {
  const [showFeedback, setShowFeedback] = useState(false)
  const [feedback, setFeedback] = useState('')

  const submitReplan = () => {
    onReplan(feedback.trim())
  }

  return (
    <div className="space-y-3 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 dark:border-amber-900 dark:bg-amber-950">
      <div className="flex items-start justify-between gap-3">
        <p className="text-sm font-medium text-amber-900 dark:text-amber-200">
          Plan ready for review
        </p>
        {answeredDecision !== undefined ? (
          <p className="shrink-0 text-sm text-amber-800 dark:text-amber-300">
            {answeredCopy[answeredDecision]}
          </p>
        ) : (
          <div className="flex shrink-0 gap-2">
            <Button variant="ghost" size="sm" onClick={onRediscover}>
              Rediscover
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setShowFeedback((v) => !v)}
            >
              Request replan
            </Button>
            <Button size="sm" onClick={onApprove}>
              Approve
            </Button>
          </div>
        )}
      </div>
      <PlanSection units={units} assumptions={assumptions} />
      {answeredDecision === undefined && showFeedback && (
        <div className="space-y-2">
          <MarkdownField
            value={feedback}
            onChange={setFeedback}
            placeholder="Optional feedback for the replan, markdown supported…"
          />
          <Button size="sm" onClick={submitReplan}>
            Send
          </Button>
        </div>
      )}
    </div>
  )
}
