// phaseTextColors is the same per-phase hue as a bare text color, for
// places that prefix a line with the phase name instead of chipping it
// (the mission timeline rows).
export const phaseTextColors: Record<string, string> = {
  discover: 'text-sky-700 dark:text-sky-400',
  plan: 'text-violet-700 dark:text-violet-400',
  generate: 'text-blue-700 dark:text-blue-400',
  prove: 'text-amber-700 dark:text-amber-400',
  result: 'text-green-700 dark:text-green-400',
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
