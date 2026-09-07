import * as React from 'react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { CopyButton } from './copy-button'

function toText(value: unknown): string {
  if (typeof value === 'string') return value
  return JSON.stringify(value, null, 2)
}

// JsonBlock is the muted inset for tool arguments, event payloads and
// raw results (sections 12, 14.5). Truncates long output behind a
// "Show all" toggle and offers a copy of the full text.
export function JsonBlock({
  value,
  density = 'trace',
  label,
  maxLines = 20,
  copy = true,
}: {
  value: unknown
  density?: 'trace' | 'prose'
  label?: string
  maxLines?: number
  copy?: boolean
}) {
  const [expanded, setExpanded] = React.useState(false)
  const text = React.useMemo(() => toText(value), [value])
  const lines = React.useMemo(() => text.split('\n'), [text])
  const truncated = lines.length > maxLines
  const shown = expanded || !truncated ? text : lines.slice(0, maxLines).join('\n')
  const hiddenCount = lines.length - maxLines

  return (
    <div className="relative">
      {copy && <CopyButton value={text} label="Copy" size="xs" className="absolute top-1.5 right-1.5" />}
      <pre
        aria-label={label}
        className={cn(
          'overflow-x-auto rounded-md bg-muted px-3 py-2 whitespace-pre',
          density === 'prose' ? 'text-code' : 'text-trace',
          copy && 'pr-9',
        )}
      >
        {shown}
      </pre>
      {truncated && (
        <Button variant="ghost" size="xs" onClick={() => setExpanded((v) => !v)}>
          {expanded ? 'Show less' : `Show all (+${hiddenCount} lines)`}
        </Button>
      )}
    </div>
  )
}
