import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { MarkdownPre } from '../MarkdownPre'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '../ui/collapsible'

// GoalSection renders a mission's full goal as collapsed-by-default
// markdown — same shape as ExploreSection, since the header only shows
// the mission's name (or a truncated goal fallback) and the full text
// moves here. Plain-text goals render unchanged: markdown of a plain
// paragraph is a no-op.
export function GoalSection({ goal }: { goal: string }) {
  return (
    <Collapsible>
      <CollapsibleTrigger className="text-xs text-muted-foreground underline underline-offset-2 hover:text-foreground">
        Show goal
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-2 rounded-lg border border-border bg-muted/30">
          <div className="prose prose-sm max-h-64 max-w-none overflow-y-auto p-3 dark:prose-invert">
            <ReactMarkdown remarkPlugins={[remarkGfm]} components={{ pre: MarkdownPre }}>
              {goal}
            </ReactMarkdown>
          </div>
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}
