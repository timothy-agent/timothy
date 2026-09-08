import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { markdownComponents, rehypePlugins, remarkPlugins } from '../../lib/markdown'
import { ChevronDown, ChevronUp } from 'lucide-react'
import { CopyButton } from '../timothy/copy-button'
import { IconButton } from '../timothy/icon-button'
import { Panel } from '../timothy/panel'

// GoalSection renders a mission's full goal inside its own Panel,
// collapsed by default; the header chevron hides and shows the body.
// Plain-text goals render unchanged: markdown of a plain paragraph is
// a no-op.
export function GoalSection({ goal }: { goal: string }) {
  const [expanded, setExpanded] = useState(false)

  const actions = (
    <>
      {expanded && <CopyButton value={goal} label="Copy goal" />}
      <IconButton
        size="xs"
        label={expanded ? 'Hide goal' : 'Show goal'}
        icon={expanded ? ChevronUp : ChevronDown}
        aria-expanded={expanded}
        onClick={() => setExpanded((v) => !v)}
      />
    </>
  )

  return (
    <Panel title="Goal" actions={actions}>
      {expanded && (
        <div className="prose max-h-96 max-w-none overflow-y-auto text-prose dark:prose-invert">
          <ReactMarkdown remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins} components={markdownComponents}>
            {goal}
          </ReactMarkdown>
        </div>
      )}
    </Panel>
  )
}
