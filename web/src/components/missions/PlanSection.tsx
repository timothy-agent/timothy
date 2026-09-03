import type { PlanAssumption, PlanUnit } from '../../api/types'

// unitBadge mirrors the harness's plan markers (missions.unitStatus,
// D-099): reviewed (a review approved it), harness-verified (harness
// evidence, awaiting review), pending. A regressed unit is pending and
// gets a separate regressed note.
export function unitBadge(u: PlanUnit): { label: string; className: string } {
  if (u.passes) {
    return { label: 'reviewed', className: 'bg-green-100 text-green-800 dark:bg-green-950 dark:text-green-300' }
  }
  if (u.harness_passed) {
    return { label: 'harness-verified', className: 'bg-green-50 text-green-700 dark:bg-green-950/60 dark:text-green-400' }
  }
  return { label: 'pending', className: 'bg-muted text-muted-foreground' }
}

export function PlanSection({ units, assumptions }: { units: PlanUnit[]; assumptions?: PlanAssumption[] }) {
  if (units.length === 0) {
    return <p className="text-sm text-muted-foreground">No plan yet.</p>
  }
  return (
    <div className="space-y-3">
      <ul className="space-y-1.5">
        {units.map((u, i) => {
          const badge = unitBadge(u)
          return (
            <li key={i} className="text-sm">
              <div className="flex items-center gap-2">
                <span
                  className={`shrink-0 rounded px-1.5 py-0.5 text-xs font-medium ${badge.className}`}
                  title={!u.passes && !u.harness_passed && u.verify_excerpt ? u.verify_excerpt : undefined}
                >
                  {badge.label}
                </span>
                <span>{u.title}</span>
                {u.regressed && !u.passes && !u.harness_passed && (
                  <span className="text-xs text-red-700 dark:text-red-400">regressed: passed before, now fails</span>
                )}
              </div>
              {u.criteria && u.criteria.length > 0 && (
                <ul className="ml-4 mt-0.5 list-disc space-y-0.5 pl-4 text-xs text-muted-foreground">
                  {u.criteria.map((c, j) => (
                    <li key={j}>{c}</li>
                  ))}
                </ul>
              )}
            </li>
          )
        })}
      </ul>
      {assumptions && assumptions.length > 0 && (
        <div>
          <h3 className="mb-1 text-xs font-semibold tracking-tight text-muted-foreground">Assumptions</h3>
          <ul className="space-y-1 text-sm">
            {assumptions.map((a, i) => (
              <li key={i} className="text-muted-foreground">
                {a.assumption} <span className="text-foreground">&rarr; {a.default}</span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}
