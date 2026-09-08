import type { PlanAssumption, PlanUnit } from '../../api/types'
import { StatusBadge } from '../timothy/status-badge'
import type { Status } from '../timothy/status'

// unitBadge mirrors the harness's plan markers (missions.unitStatus,
// D-099): reviewed (a review approved it), harness-verified (harness
// evidence, awaiting review), pending. A regressed unit is pending and
// gets a separate regressed note.
function unitBadge(u: PlanUnit): { label: string; status: Status } {
  if (u.passes) return { label: 'reviewed', status: 'success' }
  if (u.harness_passed) return { label: 'harness-verified', status: 'success' }
  return { label: 'pending', status: 'neutral' }
}

export function PlanSection({ units, assumptions }: { units: PlanUnit[]; assumptions?: PlanAssumption[] }) {
  if (units.length === 0) {
    return <p className="text-sm text-muted-foreground">No plan yet.</p>
  }
  return (
    <div className="space-y-3">
      <ul className="divide-y divide-border">
        {units.map((u, i) => {
          const badge = unitBadge(u)
          return (
            <li key={i} className="flex items-start gap-2 py-2 text-sm">
              <StatusBadge
                status={badge.status}
                label={badge.label}
                size="sm"
              />
              <div className="min-w-0 flex-1" title={!u.passes && !u.harness_passed && u.verify_excerpt ? u.verify_excerpt : undefined}>
                <div className="flex flex-wrap items-center gap-2">
                  <span>{u.title}</span>
                  {u.regressed && !u.passes && !u.harness_passed && (
                    <span className="text-xs text-destructive">regressed: passed before, now fails</span>
                  )}
                </div>
                {u.criteria && u.criteria.length > 0 && (
                  <ul className="mt-0.5 list-disc space-y-0.5 pl-4 text-xs text-muted-foreground">
                    {u.criteria.map((c, j) => (
                      <li key={j}>{c}</li>
                    ))}
                  </ul>
                )}
              </div>
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
