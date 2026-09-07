import * as React from 'react'
import {
  Brain,
  BookOpen,
  ChevronRight,
  FileText,
  Globe,
  Mail,
  Calendar,
  Plug,
  Terminal,
  Wrench,
  type LucideIcon,
} from 'lucide-react'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'
import { statusMeta, statusText, type Status } from './status'

// Category icon registry for tool calls and event kinds (section 16),
// defined once and keyed by category rather than inline per call site.
export const traceIcons: Record<
  'file' | 'shell' | 'web' | 'mail' | 'calendar' | 'memory' | 'kb' | 'connector' | 'other',
  LucideIcon
> = {
  file: FileText,
  shell: Terminal,
  web: Globe,
  mail: Mail,
  calendar: Calendar,
  memory: Brain,
  kb: BookOpen,
  connector: Plug,
  other: Wrench,
}

// TraceGroup is the recessed operational container for a turn's tool
// activity (sections 14.1, 14.5): a 28px collapsed summary row with a
// left rule, expanding to individual TraceRow children.
export function TraceGroup({
  summary,
  count,
  duration,
  defaultOpen = false,
  children,
}: {
  summary: string
  count?: number
  duration?: string
  defaultOpen?: boolean
  children?: React.ReactNode
}) {
  return (
    <Collapsible defaultOpen={defaultOpen} className="ml-1 border-l-2 border-border pl-3">
      <CollapsibleTrigger className="flex h-7 w-full items-center gap-1.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background">
        <ChevronRight
          aria-hidden
          className="size-3.5 shrink-0 text-muted-foreground transition-transform duration-100 data-[state=open]:rotate-90"
        />
        <span className="text-sm text-muted-foreground">
          {summary}
          {count != null && ` · ${count}`}
        </span>
        {duration && <span className="ml-auto text-xs tabular-nums text-muted-foreground">{duration}</span>}
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-1 space-y-0.5">{children}</CollapsibleContent>
    </Collapsible>
  )
}

// TraceRow is one operational row inside a TraceGroup: a status dot,
// an optional category icon, the humanized action, a mono target, and
// duration. Renders as a Collapsible trigger when it has children.
export function TraceRow({
  status,
  icon: CategoryIcon,
  action,
  target,
  duration,
  children,
}: {
  status: Status
  icon?: LucideIcon
  action: string
  target?: string
  duration?: string
  children?: React.ReactNode
}) {
  const meta = statusMeta[status]
  const StatusIcon = meta.icon

  const row = (
    <>
      <StatusIcon aria-hidden className={cn('size-3.5 shrink-0', statusText[status], meta.spin && 'animate-spin motion-keep')} />
      {CategoryIcon && <CategoryIcon aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />}
      <span className="text-sm text-muted-foreground">{action}</span>
      {target && <span className="truncate font-mono text-xs text-foreground">{target}</span>}
      {duration && <span className="ml-auto shrink-0 text-xs tabular-nums text-muted-foreground">{duration}</span>}
    </>
  )

  if (!children) {
    return <div className="flex h-7 items-center gap-1.5">{row}</div>
  }

  return (
    <Collapsible>
      <CollapsibleTrigger className="flex h-7 w-full items-center gap-1.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background">
        {row}
      </CollapsibleTrigger>
      <CollapsibleContent className="py-1 pl-7">{children}</CollapsibleContent>
    </Collapsible>
  )
}
