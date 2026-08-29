import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { MarkdownPre } from '../MarkdownPre'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '../ui/collapsible'

// ExploreSection renders a mission's explore_notes (set once, at the
// end of the explore phase — see driver.go's runExplore) as
// collapsed-by-default markdown, the same rendering ResultSection uses
// for last_evidence. The page only mounts this when notes is non-empty.
export function ExploreSection({ notes }: { notes: string }) {
  return (
    <Collapsible>
      <CollapsibleTrigger className="text-xs text-muted-foreground underline underline-offset-2 hover:text-foreground">
        Show exploration
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-2 rounded-lg border border-border bg-muted/30">
          <div className="prose prose-sm max-h-64 max-w-none overflow-y-auto p-3 dark:prose-invert">
            <ReactMarkdown remarkPlugins={[remarkGfm]} components={{ pre: MarkdownPre }}>
              {notes}
            </ReactMarkdown>
          </div>
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}
