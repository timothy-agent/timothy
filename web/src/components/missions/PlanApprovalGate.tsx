import { useState } from 'react'
import type { PlanAssumption, PlanUnit } from '../../api/types'
import { ApprovalCardShell } from '../timothy/approval-card'
import { Button } from '../ui/button'
import { MarkdownField } from './MarkdownField'
import { PlanSection } from './PlanSection'

// answeredCopy mirrors MissionPermissionGate's own status-line pattern:
// the mission row's phase/pause_reason only clears once the harness has
// actually acted on the decision, so the card must not look
// unanswered for that span.
const answeredCopy: Record<'approve' | 'replan' | 'rediscover', string> = {
  approve: 'Approved, moving to build…',
  replan: 'Replan requested…',
  rediscover: 'Sending back to discover…',
}

// PlanApprovalGate renders only when a mission is parked on
// phase=plan with pause_reason=approval (auto_approve_plan: false).
// Same architecture as MissionPermissionGate: callbacks as props for
// isolated testing, and once answered the buttons are replaced by a
// status line so a second click can't fire before the mission row's
// own refetch catches up.
export function PlanApprovalGate({
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
    <ApprovalCardShell
      title="Plan ready for review"
      answered={
        answeredDecision !== undefined && (
          <p className="text-sm text-muted-foreground">{answeredCopy[answeredDecision]}</p>
        )
      }
      actions={
        <>
          <Button variant="ghost" onClick={onRediscover}>
            Rediscover
          </Button>
          <Button variant="outline" onClick={() => setShowFeedback((v) => !v)}>
            Request replan
          </Button>
          <Button onClick={onApprove}>Approve</Button>
        </>
      }
    >
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
    </ApprovalCardShell>
  )
}
