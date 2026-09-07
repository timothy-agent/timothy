import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { markdownComponents, rehypePlugins, remarkPlugins } from '../../lib/markdown'
import { Button } from '../ui/button'

// ResultSection renders a mission's terminal evidence as markdown,
// clamped to a scrollable height with a "Show all" toggle to remove
// it — same rendering GoalSection/DiscoverSection use. Uses CodeBlock
// (via markdownComponents) so fenced code gets the same highlighted,
// GitHub-style treatment as chat/file markdown. The panel around it
// owns the title and copy action.
export function ResultSection({ evidence }: { evidence: string }) {
  const [expanded, setExpanded] = useState(false)

  return (
    <div className="space-y-2">
      <div className={`prose prose-sm max-w-none dark:prose-invert ${expanded ? '' : 'max-h-96 overflow-y-auto'}`}>
        <ReactMarkdown remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins} components={markdownComponents}>
          {evidence}
        </ReactMarkdown>
      </div>
      <Button variant="ghost" size="xs" onClick={() => setExpanded((v) => !v)}>
        {expanded ? 'Show less' : 'Show all'}
      </Button>
    </div>
  )
}
