import ReactMarkdown from 'react-markdown'
import { CodeBlock } from '../CodeBlock'
import { CopyButton } from '../Message'
import { rehypePlugins, remarkPlugins } from '../../lib/markdown'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '../ui/tooltip'

// Uses CodeBlock (not the plain MarkdownPre) so fenced code gets the
// same highlighted, GitHub-style treatment as chat/file markdown.
export function ResultSection({ evidence }: { evidence: string }) {
  return (
    <TooltipProvider>
      <div className="relative rounded-lg border border-border bg-muted/30">
        <div className="absolute right-2 top-2">
          <Tooltip>
            <TooltipTrigger asChild>
              <span className="inline-flex">
                <CopyButton text={evidence} label="Copy result" alwaysVisible />
              </span>
            </TooltipTrigger>
            <TooltipContent>Copy</TooltipContent>
          </Tooltip>
        </div>
        <div className="prose prose-sm max-w-none p-3 pr-10 dark:prose-invert">
          <ReactMarkdown
            remarkPlugins={remarkPlugins}
            rehypePlugins={rehypePlugins}
            components={{ pre: CodeBlock }}
          >
            {evidence}
          </ReactMarkdown>
        </div>
      </div>
    </TooltipProvider>
  )
}
