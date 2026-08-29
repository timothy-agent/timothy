import type { Row } from './options'

// statsFor computes mean/max/total for one group across the visible
// rows — "visible" here means the rows fed to the chart, not filtered
// by hidden (a hidden series still shows its stats, struck through).
function statsFor(rows: Row[], group: string): { mean: number; max: number; total: number } {
  const values = rows.map((r) => Number(r[group]) || 0)
  const total = values.reduce((n, v) => n + v, 0)
  const mean = values.length > 0 ? total / values.length : 0
  const max = values.length > 0 ? Math.max(...values) : 0
  return { mean, max, total }
}

// Grafana-style legend: color swatch, series name, Mean/Max/Total
// columns computed from rows. Plain click isolates that series;
// ctrl/cmd-click toggles it in/out of the visible set (name renders
// plain text so tests/click targets select by it).
export function StatsLegend({
  rows,
  groups,
  colorOf,
  hidden,
  onSelect,
  valueLabel,
}: {
  rows: Row[]
  groups: string[]
  colorOf: (g: string) => string
  hidden: Set<string>
  onSelect: (g: string, additive: boolean) => void
  valueLabel: (v: number) => string
}) {
  if (groups.length === 0) return null
  return (
    <div className="mt-2 overflow-x-auto">
      <table className="w-full text-xs">
        <thead>
          <tr className="text-left text-muted-foreground">
            <th className="pb-1 font-medium">Series</th>
            <th className="pb-1 pl-3 text-right font-medium">Mean</th>
            <th className="pb-1 pl-3 text-right font-medium">Max</th>
            <th className="pb-1 pl-3 text-right font-medium">Total</th>
          </tr>
        </thead>
        <tbody>
          {groups.map((g) => {
            const s = statsFor(rows, g)
            return (
              <tr key={g}>
                <td className="py-0.5">
                  <span
                    role="button"
                    tabIndex={0}
                    onClick={(e) => onSelect(g, e.ctrlKey || e.metaKey)}
                    onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && onSelect(g, false)}
                    className="flex cursor-pointer items-center gap-1.5 select-none"
                  >
                    <span className="h-2 w-2 flex-none rounded-[2.5px]" style={{ background: colorOf(g) }} />
                    <span style={{ textDecoration: hidden.has(g) ? 'line-through' : 'none' }}>{g}</span>
                  </span>
                </td>
                <td className="py-0.5 pl-3 text-right text-muted-foreground">{valueLabel(s.mean)}</td>
                <td className="py-0.5 pl-3 text-right text-muted-foreground">{valueLabel(s.max)}</td>
                <td className="py-0.5 pl-3 text-right text-muted-foreground">{valueLabel(s.total)}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
