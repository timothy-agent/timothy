export interface TooltipRow {
  key: string
  color: string
  value: string
}

export interface TooltipState {
  x: number
  y: number
  label: string
  rows: TooltipRow[]
}

export function ChartTooltip({ state }: { state: TooltipState | null }) {
  if (!state) return null
  return (
    <div
      className="pointer-events-none absolute z-20 min-w-[120px] rounded-md border border-border bg-popover px-2.5 py-2 text-xs shadow-lg"
      style={{ left: state.x + 12, top: state.y - 8 }}
    >
      <div className="mb-1 text-[11px] text-muted-foreground">{state.label}</div>
      {state.rows.map((r) => (
        <div key={r.key} className="flex items-center justify-between gap-3 py-0.5">
          <span className="flex items-center gap-1.5 text-muted-foreground">
            <span className="h-1.5 w-1.5 flex-none rounded-full" style={{ background: r.color }} />
            {r.key}
          </span>
          <span className="font-medium">{r.value}</span>
        </div>
      ))}
    </div>
  )
}
