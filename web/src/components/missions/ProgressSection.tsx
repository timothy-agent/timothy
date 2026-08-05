import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { ProgressNote } from '../../api/types'

export function ProgressSection({ notes }: { notes: ProgressNote[] }) {
  if (notes.length === 0) {
    return <p className="text-sm text-muted-foreground">No progress notes yet.</p>
  }
  return (
    <ul className="space-y-2">
      {notes.map((n, i) => (
        <li key={i} className="rounded-lg border border-border bg-muted/30">
          <div className="border-b border-border px-3 py-1.5 text-xs text-muted-foreground">
            {new Date(n.at).toLocaleString()}
          </div>
          <div className="prose prose-sm max-h-64 max-w-none overflow-y-auto p-3 dark:prose-invert">
            <ReactMarkdown remarkPlugins={[remarkGfm]}>{n.note}</ReactMarkdown>
          </div>
        </li>
      ))}
    </ul>
  )
}
