import { ChevronDown } from 'lucide-react'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '../ui/collapsible'

interface ProbeErrorDetailsProps {
  summary: string
  cause?: string
  hint?: string
  raw?: string
}

// ProbeErrorDetails renders a failure in the one error shape (contract
// 14.13): what failed, the cause in mono, what to do, and a Details
// disclosure with the raw payload.
export function ProbeErrorDetails({ summary, cause, hint, raw }: ProbeErrorDetailsProps) {
  return (
    <div className="space-y-1.5">
      <p>
        {summary}
        {cause && <span className="ml-1.5 font-mono text-xs">{cause}</span>}
      </p>
      {hint && <p className="text-xs">{hint}</p>}
      {raw && (
        <Collapsible>
          <CollapsibleTrigger className="flex items-center gap-1 text-xs font-medium underline-offset-2 hover:underline">
            Details
            <ChevronDown className="size-3" aria-hidden />
          </CollapsibleTrigger>
          <CollapsibleContent>
            <pre className="mt-1.5 max-h-40 overflow-auto rounded-md bg-muted p-2 font-mono text-xs whitespace-pre-wrap">
              {raw}
            </pre>
          </CollapsibleContent>
        </Collapsible>
      )}
    </div>
  )
}
