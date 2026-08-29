import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { CodeBlock } from '../CodeBlock'
import { CopyButton } from '../Message'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '../ui/tooltip'

// Uses CodeBlock (not the plain MarkdownPre) so fenced code gets the
// same highlighted, GitHub-style treatment as chat/file markdown.
export function ResultSection({ evidence }: { evidence: string }) {
  return (
    <TooltipProvider>
      <div className="rounded-lg border border-border bg-muted/30">
        <div className="flex items-center justify-end px-3 pt-2">
          <Tooltip>
            <TooltipTrigger asChild>
              <span className="inline-flex">
                <CopyButton text={evidence} label="Copy result" alwaysVisible />
              </span>
            </TooltipTrigger>
            <TooltipContent>Copy</TooltipContent>
          </Tooltip>
        </div>
        <div className="prose prose-sm max-w-none p-3 pt-1 dark:prose-invert">
          <ReactMarkdown remarkPlugins={[remarkGfm]} components={{ pre: CodeBlock }}>
            {evidence}
          </ReactMarkdown>
        </div>
      </div>
    </TooltipProvider>
  )
}
