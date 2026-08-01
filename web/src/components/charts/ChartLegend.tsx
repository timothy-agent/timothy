// A legend item is a plain text node beside a colored swatch — no extra
// className on the label — so click targets and tests can select by text.
export function ChartLegend({
  groups,
  colorOf,
  hidden,
  onToggle,
}: {
  groups: string[]
  colorOf: (g: string) => string
  hidden: Set<string>
  onToggle: (g: string) => void
}) {
  if (groups.length === 0) return null
  return (
    <div className="mt-2 flex flex-wrap gap-3 text-xs text-muted-foreground">
      {groups.map((g) => (
        <span
          key={g}
          role="button"
          tabIndex={0}
          onClick={() => onToggle(g)}
          onKeyDown={(e) => (e.key === 'Enter' || e.key === ' ') && onToggle(g)}
          className="flex cursor-pointer items-center gap-1.5 select-none"
        >
          <span
            className="h-2 w-2 flex-none rounded-[2.5px]"
            style={{ background: colorOf(g) }}
          />
          <span style={{ textDecoration: hidden.has(g) ? 'line-through' : 'none' }}>{g}</span>
        </span>
      ))}
    </div>
  )
}
