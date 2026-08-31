import { useState } from 'react'
import { Button } from '../ui/button'
import { Textarea } from '../ui/textarea'

// InputRequestBanner renders only when a mission has a live
// pending_input (ask_user's park, D-088). Same architecture as
// PermissionBanner/PlanApprovalBanner: callbacks as props for isolated
// testing, and once answered the inputs are replaced by a status line
// so a second submit can't fire before the mission row's own refetch
// catches up.
export function InputRequestBanner({
  question,
  kind,
  options,
  proposedDefault,
  answered,
  onAnswer,
  timeoutSeconds,
  askedAt,
}: {
  question: string
  kind: 'mcq' | 'yes_no' | 'open'
  options?: string[]
  proposedDefault: string
  answered?: string
  onAnswer: (answer: string) => void
  // timeoutSeconds is the operator-configured global ask_timeout_seconds
  // (D-088): an ask_user park auto-answers with proposedDefault after
  // this many seconds. undefined/0 means no timeout: the mission waits
  // forever, same convention as PermissionBanner's timeoutSeconds.
  timeoutSeconds?: number
  // askedAt is when this question was recorded (PendingInput.AskedAt),
  // needed alongside timeoutSeconds to render a countdown; only
  // rendered when both are resolvable client-side (mirrors #445's
  // PermissionBanner choice not to compute a live remaining-time value,
  // just a static line).
  askedAt?: string
}) {
  const [openAnswer, setOpenAnswer] = useState('')

  return (
    <div className="space-y-3 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 dark:border-amber-900 dark:bg-amber-950">
      <div className="space-y-1">
        <p className="text-sm font-medium text-amber-900 dark:text-amber-200">{question}</p>
        {typeof timeoutSeconds === 'number' && timeoutSeconds > 0 && askedAt && (
          <p className="text-xs text-amber-700 dark:text-amber-400">
            Auto-answers with the proposed default if unanswered for {timeoutSeconds}s
          </p>
        )}
      </div>

      {answered !== undefined ? (
        <p className="text-sm text-amber-800 dark:text-amber-300">Answered: {answered}</p>
      ) : kind === 'mcq' ? (
        <div className="flex flex-wrap gap-2">
          {(options ?? []).map((opt) => (
            <Button
              key={opt}
              variant={opt === proposedDefault ? 'default' : 'outline'}
              size="sm"
              onClick={() => onAnswer(opt)}
            >
              {opt}
              {opt === proposedDefault && ' (proposed)'}
            </Button>
          ))}
        </div>
      ) : kind === 'yes_no' ? (
        <div className="flex gap-2">
          <Button
            variant={proposedDefault === 'yes' ? 'default' : 'outline'}
            size="sm"
            onClick={() => onAnswer('yes')}
          >
            Yes{proposedDefault === 'yes' && ' (proposed)'}
          </Button>
          <Button
            variant={proposedDefault === 'no' ? 'default' : 'outline'}
            size="sm"
            onClick={() => onAnswer('no')}
          >
            No{proposedDefault === 'no' && ' (proposed)'}
          </Button>
        </div>
      ) : (
        <div className="space-y-2">
          <Textarea
            placeholder={proposedDefault || 'Your answer…'}
            value={openAnswer}
            onChange={(e) => setOpenAnswer(e.target.value)}
          />
          <Button size="sm" onClick={() => onAnswer(openAnswer.trim() || proposedDefault)}>
            Send
          </Button>
        </div>
      )}
    </div>
  )
}
