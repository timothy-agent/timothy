import type { PlanAssumption, PlanUnit } from '../../api/types'

export function PlanSection({ units, assumptions }: { units: PlanUnit[]; assumptions?: PlanAssumption[] }) {
  if (units.length === 0) {
    return <p className="text-sm text-muted-foreground">No plan yet.</p>
  }
  return (
    <div className="space-y-3">
      <ul className="space-y-1.5">
        {units.map((u, i) => (
          <li key={i} className="flex items-center gap-2 text-sm">
            <span
              className={`shrink-0 rounded px-1.5 py-0.5 text-xs font-medium ${
                u.passes
                  ? 'bg-green-100 text-green-800 dark:bg-green-950 dark:text-green-300'
                  : 'bg-muted text-muted-foreground'
              }`}
            >
              {u.passes ? 'verified' : 'pending'}
            </span>
            <span>{u.title}</span>
          </li>
        ))}
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
