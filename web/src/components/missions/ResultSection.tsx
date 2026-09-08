import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { markdownComponents, rehypePlugins, remarkPlugins } from '../../lib/markdown'
import { ChevronDown, ChevronUp } from 'lucide-react'
import { IconButton } from '../timothy/icon-button'

// ResultSection renders a mission's terminal evidence as markdown,
// clamped to a scrollable height with a chevron toggle to remove
// it — same rendering GoalSection/DiscoverSection use. Uses CodeBlock
// (via markdownComponents) so fenced code gets the same highlighted,
// GitHub-style treatment as chat/file markdown. The panel around it
// owns the title and copy action.
export function ResultSection({ evidence }: { evidence: string }) {
  const [expanded, setExpanded] = useState(false)

  return (
    <div className="space-y-2">
      <div className={`prose max-w-none dark:prose-invert ${expanded ? '' : 'max-h-96 overflow-y-auto'}`}>
        <ReactMarkdown remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins} components={markdownComponents}>
          {evidence}
        </ReactMarkdown>
      </div>
      <IconButton
        size="xs"
        label={expanded ? 'Show less' : 'Show all'}
        icon={expanded ? ChevronUp : ChevronDown}
        aria-expanded={expanded}
        onClick={() => setExpanded((v) => !v)}
      />
    </div>
  )
}
