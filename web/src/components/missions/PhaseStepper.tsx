import { Check, ChevronRight, CircleX } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import type { MissionEvent } from '@/api/types'
import { cn } from '@/lib/utils'
import { phaseLabel } from '@/lib/phaseColors'

// Mission phase pipeline (D-086, issue #455), current names only:
// normalizePhase maps the pre-rename ones (explore/execute/review)
// onto these via phaseLabel before indexing.
const missionPhases = ['discover', 'plan', 'generate', 'prove', 'result'] as const
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

// failedPhaseFromEvents returns the pipeline phase a failed mission was
// in when it stopped: the latest mission.phase_started payload, else
// the phase the mission was born in (generate for light missions,
// discover otherwise, neither of which emits phase_started).
export function failedPhaseFromEvents(events: MissionEvent[], light?: boolean): MissionPhase {
  for (let i = events.length - 1; i >= 0; i--) {
    const e = events[i]
    if (e.kind !== 'mission.phase_started') continue
    const phase = (e.payload as { phase?: unknown } | null)?.phase
    if (typeof phase === 'string') {
      const n = normalizePhase(phase)
      if (n !== 'done' && n !== 'failed') return n
    }
  }
  return light ? 'generate' : 'discover'
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
// (contract 14.10): completed steps get a green check icon, the current
// step is emphasised, upcoming steps are muted. A failed mission marks
// the step it died in with an X (failedAt) so the reader sees how far
// it got. The mission status itself lives in the page header badge. No hue per phase;
// understandable without colour via icon, weight and aria-current.
// Light missions collapse to a single "Generate" step.
export function PhaseStepper({
  phase,
  light,
  failedAt,
  className,
}: {
  phase: string
  light?: boolean
  // Pipeline phase the mission failed in; only read when phase is failed.
  failedAt?: MissionPhase
  className?: string
}) {
  if (light) {
    const lightDone = phase === 'done'
    const lightFailed = phase === 'failed'
    return (
      <ol aria-label="Mission phases" className={cn('flex flex-wrap items-center gap-x-2 gap-y-1 text-sm', className)}>
        <li aria-current={lightDone || lightFailed ? undefined : 'step'} className="flex items-center gap-2">
          <span className={cn('flex items-center gap-1.5 font-medium', lightFailed ? 'text-destructive' : 'text-foreground')}>
            {lightDone && <Check aria-hidden className="size-3.5 text-good" />}
            {lightFailed && <CircleX aria-hidden className="size-3.5" />}
            Generate
          </span>
          <Badge variant="secondary" size="sm">
            light
          </Badge>
        </li>
      </ol>
    )
  }

  const terminal = phase === 'done' || phase === 'failed'
  const failed = phase === 'failed'
  const failedIndex = failed && failedAt ? missionPhases.indexOf(failedAt) : -1
  const currentIndex = terminal ? missionPhases.length - 1 : phaseIndex(phase)

  return (
    <ol aria-label="Mission phases" className={cn('flex flex-wrap items-center gap-x-2 gap-y-1 text-sm', className)}>
      {missionPhases.map((step, i) => {
        const isCurrent = !terminal && i === currentIndex
        const isFailed = i === failedIndex
        const isDone = failed ? failedIndex > i : terminal || i < currentIndex
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
                isFailed && 'font-medium text-destructive',
                !isDone && !isCurrent && !isFailed && 'text-muted-foreground',
              )}
            >
              {isDone && <Check aria-hidden className="size-3.5 text-good" />}
              {isFailed && <CircleX aria-hidden className="size-3.5" />}
              {label}
            </span>
          </li>
        )
      })}
    </ol>
  )
}
