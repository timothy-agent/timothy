// phaseColors is the one shared color-per-phase map (issue #473): a
// mission's discover/plan/generate/prove/result phase reads with the
// same color wherever it's chipped, following MissionCard's own
// bg-X-100/dark:bg-X-950 status-pill convention. done/failed/legacy
// (explore/execute/review, pre-D-086 rename) fall back to phaseColors'
// caller-side default rather than being listed here: this map only
// covers the five phases the timeline chips.
export const phaseColors: Record<string, string> = {
  discover: 'bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300',
  plan: 'bg-violet-100 text-violet-800 dark:bg-violet-950 dark:text-violet-300',
  generate: 'bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-300',
  prove: 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300',
  result: 'bg-green-100 text-green-800 dark:bg-green-950 dark:text-green-300',
}

// phaseLabel normalizes a phase string for display: the pre-D-086
// names (explore/execute/review) a historical mission's events may
// still carry map to their current equivalents, same mapping
// statemachine.go's parsePhase applies on read.
const legacyPhaseNames: Record<string, string> = {
  explore: 'discover',
  execute: 'generate',
  review: 'prove',
}

export function phaseLabel(phase: string): string {
  return legacyPhaseNames[phase] ?? phase
}
