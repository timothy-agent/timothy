import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { markdownComponents, rehypePlugins, remarkPlugins } from '../../lib/markdown'
import { Button } from '../ui/button'
import { CopyButton } from '../timothy/copy-button'
import { Panel } from '../timothy/panel'

// GoalSection renders a mission's full goal inside its own Panel,
// clamped to three lines when collapsed and fully expanded on toggle.
// Plain-text goals render unchanged: markdown of a plain paragraph is
// a no-op.
export function GoalSection({ goal }: { goal: string }) {
  const [expanded, setExpanded] = useState(false)

  const actions = (
    <>
      {expanded && <CopyButton value={goal} label="Copy goal" />}
      <Button variant="ghost" size="xs" onClick={() => setExpanded((v) => !v)}>
        {expanded ? 'Hide goal' : 'Show goal'}
      </Button>
    </>
  )

  return (
    <Panel title="Goal" actions={actions}>
      <div
        className={
          expanded
            ? 'prose prose-sm max-h-96 max-w-none overflow-y-auto text-prose dark:prose-invert'
            : 'prose prose-sm line-clamp-3 max-w-none text-prose dark:prose-invert'
        }
      >
        <ReactMarkdown remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins} components={markdownComponents}>
          {goal}
        </ReactMarkdown>
      </div>
    </Panel>
  )
}
