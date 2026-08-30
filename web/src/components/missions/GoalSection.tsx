import ReactMarkdown from 'react-markdown'
import { CodeBlock } from '../CodeBlock'
import { CopyButton } from '../Message'
import { rehypePlugins, remarkPlugins } from '../../lib/markdown'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '../ui/collapsible'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '../ui/tooltip'

// GoalSection renders a mission's full goal as collapsed-by-default
// markdown — same shape as ExploreSection, since the header only shows
// the mission's name (or a truncated goal fallback) and the full text
// moves here. Plain-text goals render unchanged: markdown of a plain
// paragraph is a no-op. The copy button overlays the content block's
// top-right corner, same placement as ExploreSection/ResultSection's.
export function GoalSection({ goal }: { goal: string }) {
  return (
    <TooltipProvider>
      <Collapsible>
        <CollapsibleTrigger className="text-xs text-muted-foreground underline underline-offset-2 hover:text-foreground">
          Show goal
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div className="relative mt-2 rounded-lg border border-border bg-muted/30">
            <div className="absolute right-2 top-2">
              <Tooltip>
                <TooltipTrigger asChild>
                  <span className="inline-flex">
                    <CopyButton text={goal} label="Copy goal" alwaysVisible />
                  </span>
                </TooltipTrigger>
                <TooltipContent>Copy</TooltipContent>
              </Tooltip>
            </div>
            <div className="prose prose-sm max-h-64 max-w-none overflow-y-auto p-3 pr-10 dark:prose-invert">
              <ReactMarkdown
                remarkPlugins={remarkPlugins}
                rehypePlugins={rehypePlugins}
                components={{ pre: CodeBlock }}
              >
                {goal}
              </ReactMarkdown>
            </div>
          </div>
        </CollapsibleContent>
      </Collapsible>
    </TooltipProvider>
  )
}
