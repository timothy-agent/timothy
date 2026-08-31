import { useState } from 'react'
import ReactMarkdown from 'react-markdown'
import { CodeBlock } from '../CodeBlock'
import { rehypePlugins, remarkPlugins } from '../../lib/markdown'
import { Button } from '../ui/button'
import { Textarea } from '../ui/textarea'

// MarkdownField is a controlled textarea with a Write/Preview toggle,
// reusing GoalSection's markdown stack for Preview. Shared by
// InputRequestBanner's open kind, the Intervene modal, and
// PlanApprovalBanner's replan feedback, so all three text inputs a
// mission operator can markdown-format render the same way the goal
// itself does.
export function MarkdownField({
  value,
  onChange,
  placeholder,
  disabled,
  rows,
}: {
  value: string
  onChange: (value: string) => void
  placeholder?: string
  disabled?: boolean
  rows?: number
}) {
  const [tab, setTab] = useState<'write' | 'preview'>('write')

  return (
    <div className="space-y-1.5">
      <div className="flex gap-1">
        <Button
          type="button"
          variant={tab === 'write' ? 'secondary' : 'ghost'}
          size="xs"
          onClick={() => setTab('write')}
        >
          Write
        </Button>
        <Button
          type="button"
          variant={tab === 'preview' ? 'secondary' : 'ghost'}
          size="xs"
          onClick={() => setTab('preview')}
        >
          Preview
        </Button>
      </div>
      {tab === 'write' ? (
        <Textarea
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder}
          disabled={disabled}
          rows={rows}
        />
      ) : value.trim() ? (
        <div className="prose prose-sm min-h-16 max-w-none rounded-lg border border-input px-2.5 py-2 dark:prose-invert">
          <ReactMarkdown remarkPlugins={remarkPlugins} rehypePlugins={rehypePlugins} components={{ pre: CodeBlock }}>
            {value}
          </ReactMarkdown>
        </div>
      ) : (
        <div className="flex min-h-16 items-center rounded-lg border border-input px-2.5 py-2 text-sm text-muted-foreground">
          Nothing to preview
        </div>
      )}
    </div>
  )
}
