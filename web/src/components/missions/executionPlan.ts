import type { ExecutionPlanEntry, ExecutionPlanPhase } from '../../api/types'

// unusablePhases lists the phases that will run on a chain whose
// entries are all unusable (D-100): the create form blocks submit on
// it and the server rejects the same request with route_unusable. A
// phase with no entries (resolve failure, empty chain) is left out,
// matching the server's own gate.
export function unusablePhases(plan: ExecutionPlanPhase[] | null): ExecutionPlanPhase[] {
  return (plan ?? []).filter(
    (p) => !p.skipped && !!p.route && p.entries.length > 0 && !p.entries.some((e) => e.usable),
  )
}

// unusableReason is the first skip reason among an unusable chain's
// entries, or the server's generic reason.
export function unusableReason(entries: ExecutionPlanEntry[]): string {
  return entries.find((e) => e.skip_reason)?.skip_reason ?? 'no usable provider for this route'
}
