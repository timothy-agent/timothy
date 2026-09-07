import { ToolCallCard } from './chat/ToolCallCard'
import { SheetContent, SheetHeader, SheetTitle } from './ui/sheet'
import { totalDuration } from '../lib/activity'
import type { AssistantState } from '../lib/chat'
import { formatDuration } from '../lib/format'

// ActivityPanel is the full detail behind a message's Activity button:
// reasoning text plus every tool call, rendered via ToolCallCard at
// operational density (contract 14.5). Chat.tsx hosts one Sheet
// instance and re-derives the item from live state each render, so
// the panel updates in real time while a turn streams.
export function ActivityPanel({ msg }: { msg: AssistantState }) {
  const sum = totalDuration(msg.tools)
  return (
    <SheetContent side="right" className="data-[side=right]:sm:max-w-lg">
      <SheetHeader>
        <SheetTitle>Activity</SheetTitle>
        {msg.tools.length > 0 && (
          <p className="text-xs text-muted-foreground">
            {msg.tools.length} tool{msg.tools.length === 1 ? '' : 's'}
            {sum > 0 && ` · ${formatDuration(sum)}`}
          </p>
        )}
      </SheetHeader>
      <div className="flex-1 space-y-3 overflow-y-auto px-4 pb-4">
        {msg.reasoning !== '' && (
          <pre className="font-mono text-xs leading-[18px] whitespace-pre-wrap text-muted-foreground">
            {msg.reasoning}
          </pre>
        )}
        <div className="divide-y divide-border">
          {msg.tools.map((t) => (
            <ToolCallCard key={t.id} run={t} density="operational" />
          ))}
        </div>
      </div>
    </SheetContent>
  )
}
