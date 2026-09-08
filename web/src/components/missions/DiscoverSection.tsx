import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { markdownComponents, rehypePlugins, remarkPlugins } from '../../lib/markdown'
import { Button } from '../ui/button'
import { CopyButton } from '../timothy/copy-button'
import { Panel } from '../timothy/panel'

// DiscoverSection renders a mission's discover_notes (set once, at the
// end of the discover phase, see driver.go's runDiscover) inside its
// own Panel, clamped to three lines when collapsed and fully expanded
// on toggle, the same rendering ResultSection uses for last_evidence.
// The page only mounts this when notes is non-empty.
export function DiscoverSection({ notes }: { notes: string }) {
  const [expanded, setExpanded] = useState(false)

  const actions = (
    <>
      {expanded && <CopyButton value={notes} label="Copy discovery notes" />}
      <Button variant="ghost" size="xs" onClick={() => setExpanded((v) => !v)}>
        {expanded ? 'Hide discovery' : 'Show discovery'}
      </Button>
    </>
  )

  return (
    <Panel title="Discover" actions={actions}>
      <div
        className={
          expanded
            ? 'prose prose-sm max-h-96 max-w-none overflow-y-auto text-prose dark:prose-invert'
            : 'prose prose-sm line-clamp-3 max-w-none text-prose dark:prose-invert'
        }
      >
        <ReactMarkdown remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins} components={markdownComponents}>
          {notes}
        </ReactMarkdown>
      </div>
    </Panel>
  )
}
