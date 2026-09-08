import { Check, ChevronRight } from 'lucide-react'

import { StatusBadge } from '@/components/timothy/status-badge'
import { statusMeta, type Status } from '@/components/timothy/status'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { phaseLabel } from '@/lib/phaseColors'

// Mission phase pipeline (D-086, issue #455), current names only:
// normalizePhase maps the pre-rename ones (explore/execute/review)
// onto these via phaseLabel before indexing.
export const missionPhases = ['discover', 'plan', 'generate', 'prove', 'result'] as const
export type MissionPhase = (typeof missionPhases)[number]

// normalizePhase maps a raw mission phase string (current or legacy)
// to one of the five pipeline phases, or the two terminal states.
export function normalizePhase(phase: string): MissionPhase | 'done' | 'failed' {
  if (phase === 'done' || phase === 'failed') return phase
  const label = phaseLabel(phase)
  return (missionPhases as readonly string[]).includes(label) ? (label as MissionPhase) : 'discover'
}

// phaseIndex returns the step position (0..4) for a mission phase, 5
// for a terminal phase (done/failed, all five steps read completed),
// or -1 for an unrecognized value.
export function phaseIndex(phase: string): number {
  if (phase === 'done' || phase === 'failed') return 5
  const label = phaseLabel(phase)
  const i = (missionPhases as readonly string[]).indexOf(label)
  return i
}

// phaseStepText renders the compact "Phase · n of 5" line used on
// mission cards (14.10).
export function phaseStepText(mission: { phase: string; light?: boolean; flow?: string }): string {
  if (mission.phase === 'done') return 'Done'
  if (mission.phase === 'failed') return 'Failed'
  if (mission.light) return 'Generate · light'
  const label = phaseLabel(mission.phase)
  const i = missionPhases.indexOf(label as MissionPhase)
  if (i < 0) return label
  const stepLabel = label.charAt(0).toUpperCase() + label.slice(1)
  return `${stepLabel} · ${i + 1} of 5`
}

// PhaseStepper renders the mission phase pipeline as a stepper
// (contract 14.10): completed steps get a check icon, the current
// step carries the status badge, upcoming steps are muted. No hue per
// phase; understandable without colour via icon, weight and
// aria-current. Light missions collapse to a single "Generate" step.
export function PhaseStepper({
  phase,
  status,
  light,
  className,
}: {
  phase: string
  status: Status
  light?: boolean
  className?: string
}) {
  if (light) {
    return (
      <ol aria-label="Mission phases" className={cn('flex flex-wrap items-center gap-x-2 gap-y-1 text-sm', className)}>
        <li aria-current="step" className="flex items-center gap-2">
          <span className="font-medium text-foreground">Generate</span>
          <Badge variant="secondary" size="sm">
            light
          </Badge>
        </li>
      </ol>
    )
  }

  const terminal = phase === 'done' || phase === 'failed'
  const failed = phase === 'failed'
  const currentIndex = terminal ? missionPhases.length - 1 : phaseIndex(phase)
  const terminalLabel = phase === 'done' ? 'Done' : 'Failed'

  return (
    <ol aria-label="Mission phases" className={cn('flex flex-wrap items-center gap-x-2 gap-y-1 text-sm', className)}>
      {missionPhases.map((step, i) => {
        const isCurrent = !terminal && i === currentIndex
        const isDone = !failed && (terminal || i < currentIndex)
        const label = step.charAt(0).toUpperCase() + step.slice(1)
        return (
          <li key={step} className="flex items-center gap-2">
            {i > 0 && <ChevronRight aria-hidden className="size-3.5 text-muted-foreground" />}
            <span
              aria-current={isCurrent ? 'step' : undefined}
              className={cn(
                'flex items-center gap-1.5',
                isDone && 'text-foreground',
                isCurrent && 'font-medium text-foreground',
                !isDone && !isCurrent && 'text-muted-foreground',
              )}
            >
              {isDone && <Check aria-hidden className="size-3.5" />}
              {label}
              {isCurrent && <StatusBadge status={status} label={statusMeta[status].label} size="sm" />}
            </span>
          </li>
        )
      })}
      {terminal && <StatusBadge status={status} label={terminalLabel} size="sm" />}
    </ol>
  )
}
