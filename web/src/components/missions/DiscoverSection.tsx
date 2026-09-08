import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { markdownComponents, rehypePlugins, remarkPlugins } from '../../lib/markdown'
import { ChevronDown, ChevronUp } from 'lucide-react'
import { CopyButton } from '../timothy/copy-button'
import { IconButton } from '../timothy/icon-button'
import { Panel } from '../timothy/panel'

// DiscoverSection renders a mission's discover_notes (set once, at the
// end of the discover phase, see driver.go's runDiscover) inside its
// own Panel, collapsed by default with a header chevron that shows the body,
// the same rendering ResultSection uses for last_evidence.
// The page only mounts this when notes is non-empty.
export function DiscoverSection({ notes }: { notes: string }) {
  const [expanded, setExpanded] = useState(false)

  const actions = (
    <>
      {expanded && <CopyButton value={notes} label="Copy discovery notes" />}
      <IconButton
        size="xs"
        label={expanded ? 'Hide discovery' : 'Show discovery'}
        icon={expanded ? ChevronUp : ChevronDown}
        aria-expanded={expanded}
        onClick={() => setExpanded((v) => !v)}
      />
    </>
  )

  return (
    <Panel title="Discover" actions={actions}>
      {expanded && (
        <div className="prose max-h-96 max-w-none overflow-y-auto text-prose dark:prose-invert">
          <ReactMarkdown remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins} components={markdownComponents}>
            {notes}
          </ReactMarkdown>
        </div>
      )}
    </Panel>
  )
}
