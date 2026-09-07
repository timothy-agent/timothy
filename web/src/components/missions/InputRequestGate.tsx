import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { markdownComponents, rehypePlugins, remarkPlugins } from '../../lib/markdown'
import { ApprovalCardShell } from '../timothy/approval-card'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { MarkdownField } from './MarkdownField'

// InputRequestGate renders only when a mission has a live
// pending_input (ask_user's park, D-088). Same architecture as
// MissionPermissionGate/PlanApprovalGate: callbacks as props for
// isolated testing, and once answered the inputs are replaced by a
// status line so a second submit can't fire before the mission row's
// own refetch catches up.
export function InputRequestGate({
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
  // forever, same convention as MissionPermissionGate's timeoutSeconds.
  timeoutSeconds?: number
  // askedAt is when this question was recorded (PendingInput.AskedAt),
  // needed alongside timeoutSeconds to render a countdown; only
  // rendered when both are resolvable client-side (mirrors #445's
  // MissionPermissionGate choice not to compute a live remaining-time
  // value, just a static line).
  askedAt?: string
}) {
  const [openAnswer, setOpenAnswer] = useState('')

  const defaultLine =
    kind === 'yes_no' ? (proposedDefault === 'yes' ? 'Yes' : 'No') : proposedDefault

  return (
    <ApprovalCardShell
      title="Timothy has a question"
      consequence={
        answered === undefined
          ? typeof timeoutSeconds === 'number' && timeoutSeconds > 0 && askedAt
            ? `Auto-answers with the proposed default (${defaultLine}) if unanswered for ${timeoutSeconds}s`
            : `Auto-answers with the proposed default (${defaultLine}) if unanswered`
          : undefined
      }
      answered={
        answered !== undefined && <p className="text-sm text-muted-foreground">Answered: {answered}</p>
      }
      actions={
        kind === 'mcq' ? (
          (options ?? []).map((opt) => (
            <Button key={opt} variant={opt === proposedDefault ? 'default' : 'outline'} onClick={() => onAnswer(opt)}>
              {opt}
              {opt === proposedDefault && (
                <Badge variant="secondary" size="sm">
                  default
                </Badge>
              )}
            </Button>
          ))
        ) : kind === 'yes_no' ? (
          <>
            <Button variant={proposedDefault === 'yes' ? 'default' : 'outline'} onClick={() => onAnswer('yes')}>
              Yes
              {proposedDefault === 'yes' && (
                <Badge variant="secondary" size="sm">
                  default
                </Badge>
              )}
            </Button>
            <Button variant={proposedDefault === 'no' ? 'default' : 'outline'} onClick={() => onAnswer('no')}>
              No
              {proposedDefault === 'no' && (
                <Badge variant="secondary" size="sm">
                  default
                </Badge>
              )}
            </Button>
          </>
        ) : (
          <div className="w-full space-y-2">
            <MarkdownField
              value={openAnswer}
              onChange={setOpenAnswer}
              placeholder={proposedDefault || 'Your answer, markdown supported…'}
            />
            <Button size="sm" onClick={() => onAnswer(openAnswer.trim() || proposedDefault)}>
              Send
            </Button>
          </div>
        )
      }
    >
      <div className="prose prose-sm max-w-none dark:prose-invert">
        <ReactMarkdown remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins} components={markdownComponents}>
          {question}
        </ReactMarkdown>
      </div>
    </ApprovalCardShell>
  )
}
