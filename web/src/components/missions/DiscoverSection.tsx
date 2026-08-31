import ReactMarkdown from 'react-markdown'
import { CodeBlock } from '../CodeBlock'
import { CopyButton } from '../Message'
import { rehypePlugins, remarkPlugins } from '../../lib/markdown'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '../ui/collapsible'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '../ui/tooltip'

// DiscoverSection renders a mission's discover_notes (set once, at the
// end of the discover phase, see driver.go's runDiscover) as
// collapsed-by-default markdown, the same rendering ResultSection uses
// for last_evidence. The page only mounts this when notes is non-empty.
// Uses CodeBlock (not the plain MarkdownPre) so fenced code gets the
// same highlighted, GitHub-style treatment as chat/file markdown. The
// copy button sits inside the content block, same placement as
// ResultSection's, not in the collapsible's trigger row.
export function DiscoverSection({ notes }: { notes: string }) {
  return (
    <TooltipProvider>
      <Collapsible>
        <CollapsibleTrigger className="text-xs text-muted-foreground underline underline-offset-2 hover:text-foreground">
          Show discovery
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div className="relative mt-2 rounded-lg border border-border bg-muted/30">
            <div className="absolute right-2 top-2">
              <Tooltip>
                <TooltipTrigger asChild>
                  <span className="inline-flex">
                    <CopyButton text={notes} label="Copy discovery notes" alwaysVisible />
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
                {notes}
              </ReactMarkdown>
            </div>
          </div>
        </CollapsibleContent>
      </Collapsible>
    </TooltipProvider>
  )
}
