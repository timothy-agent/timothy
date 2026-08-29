import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { MarkdownPre } from '../MarkdownPre'

export function ResultSection({ evidence }: { evidence: string }) {
  return (
    <div className="rounded-lg border border-border bg-muted/30">
      <div className="prose prose-sm max-w-none p-3 dark:prose-invert">
        <ReactMarkdown remarkPlugins={[remarkGfm]} components={{ pre: MarkdownPre }}>
          {evidence}
        </ReactMarkdown>
      </div>
    </div>
  )
}
