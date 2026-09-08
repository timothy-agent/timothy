import { Circle, CircleCheck, CircleX, Hand, Loader2, TriangleAlert, type LucideIcon } from 'lucide-react'

// The one status model (design contract section 13.1). Every domain
// status collapses into one of these six values; components render
// from this, never from a bespoke colour map.
export type Status = 'neutral' | 'working' | 'waiting' | 'success' | 'warning' | 'error'

export const statuses: readonly Status[] = ['neutral', 'working', 'waiting', 'success', 'warning', 'error']

export const statusMeta: Record<
  Status,
  { label: string; icon: LucideIcon; tone: 'neutral' | 'info' | 'warning' | 'good' | 'destructive'; spin: boolean }
> = {
  neutral: { label: 'Neutral', icon: Circle, tone: 'neutral', spin: false },
  working: { label: 'Working', icon: Loader2, tone: 'info', spin: true },
  waiting: { label: 'Waiting', icon: Hand, tone: 'warning', spin: false },
  success: { label: 'Success', icon: CircleCheck, tone: 'good', spin: false },
  warning: { label: 'Warning', icon: TriangleAlert, tone: 'warning', spin: false },
  error: { label: 'Error', icon: CircleX, tone: 'destructive', spin: false },
}

// Text colour per status, for icons and labels rendered outside a badge.
export const statusText: Record<Status, string> = {
  neutral: 'text-muted-foreground',
  working: 'text-info',
  waiting: 'text-warning',
  success: 'text-good',
  warning: 'text-warning',
  error: 'text-destructive',
}

// Solid background for dots and other filled indicators.
export const statusSolidBg: Record<Status, string> = {
  neutral: 'bg-muted-foreground',
  working: 'bg-info',
  waiting: 'bg-warning',
  success: 'bg-good',
  warning: 'bg-warning',
  error: 'bg-destructive',
}


// missionStatus implements the domain mapping table (contract 13.2)
// for a mission's phase/status pair. Mirrors MissionCard's own
// phase === 'done' / 'failed' precedence: a terminal phase overrides
// the raw status string.
export function missionStatus(input: { phase?: string; status: string }): Status {
  const { phase, status } = input
  if (phase === 'done') return 'success'
  if (phase === 'failed') {
    return status === 'cancelled' ? 'neutral' : 'error'
  }
  if (status === 'idle') return 'neutral'
  if (status === 'error') return 'error'
  if (status === 'paused') return 'warning'
  if (status === 'waiting_for_input') return 'waiting'
  if (status === 'done') return 'success'
  // Any other phase (discover/plan/generate/prove, plus the legacy
  // explore/execute/review names) with a live status is running work.
  return 'working'
}

// toolCallStatus implements the tool-call row of the same mapping table.
export function toolCallStatus(s: 'running' | 'ok' | 'denied' | 'error' | 'blocked'): Status {
  if (s === 'running') return 'working'
  if (s === 'ok') return 'success'
  if (s === 'denied') return 'neutral'
  return 'error'
}
