import ReactMarkdown from 'react-markdown'
import { CodeBlock } from '../CodeBlock'
import { CopyButton } from '../Message'
import { rehypePlugins, remarkPlugins } from '../../lib/markdown'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '../ui/collapsible'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '../ui/tooltip'

// ExploreSection renders a mission's explore_notes (set once, at the
// end of the explore phase — see driver.go's runExplore) as
// collapsed-by-default markdown, the same rendering ResultSection uses
// for last_evidence. The page only mounts this when notes is non-empty.
// Uses CodeBlock (not the plain MarkdownPre) so fenced code gets the
// same highlighted, GitHub-style treatment as chat/file markdown. The
// copy button sits inside the content block, same placement as
// ResultSection's, not in the collapsible's trigger row.
export function ExploreSection({ notes }: { notes: string }) {
  return (
    <TooltipProvider>
      <Collapsible>
        <CollapsibleTrigger className="text-xs text-muted-foreground underline underline-offset-2 hover:text-foreground">
          Show exploration
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div className="mt-2 rounded-lg border border-border bg-muted/30">
            <div className="flex items-center justify-end px-3 pt-2">
              <Tooltip>
                <TooltipTrigger asChild>
                  <span className="inline-flex">
                    <CopyButton text={notes} label="Copy exploration notes" alwaysVisible />
                  </span>
                </TooltipTrigger>
                <TooltipContent>Copy</TooltipContent>
              </Tooltip>
            </div>
            <div className="prose prose-sm max-h-64 max-w-none overflow-y-auto p-3 pt-1 dark:prose-invert">
              <ReactMarkdown
                remarkPlugins={remarkPlugins}
                rehypePlugins={rehypePlugins}
                components={{ pre: CodeBlock }}
              >
                {notes}
              </ReactMarkdown>
            </div>
          </div>
        </CollapsibleContent>
      </Collapsible>
    </TooltipProvider>
  )
}
