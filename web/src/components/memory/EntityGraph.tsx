import { useMemo, useState } from 'react'
import type { EntityGraphData } from '../../api/types'
import { layoutGraph } from './graphLayout'

// Node fill per entity kind — mid hues that read on both themes,
// consistent with the page's raw-palette convention (TypeBadge).
const kindFill: Record<string, string> = {
  person: 'fill-blue-500/70',
  project: 'fill-violet-500/70',
  service: 'fill-cyan-500/70',
  preference: 'fill-emerald-500/70',
  decision: 'fill-amber-500/70',
  topic: 'fill-rose-500/70',
  place: 'fill-lime-500/70',
}

const W = 720
const H = 520

// EntityGraph is the presentational SVG: type-clustered nodes sized
// by memory count, co-occurrence edges weighted by shared memories.
// Click a node to inspect it; click the background to clear.
export function EntityGraph({
  data,
  selectedId,
  onSelect,
}: {
  data: EntityGraphData
  selectedId: string | null
  onSelect: (id: string | null) => void
}) {
  const [hoverId, setHoverId] = useState<string | null>(null)

  const { positions, edges } = useMemo(
    () => layoutGraph(data.entities, data.edges, W, H),
    [data],
  )
  const neighbors = useMemo(() => {
    const map = new Map<string, Set<string>>()
    for (const e of edges) {
      if (!map.has(e.src)) map.set(e.src, new Set())
      if (!map.has(e.dst)) map.set(e.dst, new Set())
      map.get(e.src)!.add(e.dst)
      map.get(e.dst)!.add(e.src)
    }
    return map
  }, [edges])

  if (data.entities.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        No entities yet — the graph builds as Timothy extracts memories.
      </p>
    )
  }

  const focus = hoverId ?? selectedId
  const maxWeight = Math.max(1, ...edges.map((e) => e.weight))
  const present = [...new Set(data.entities.map((n) => n.type))]

  return (
    <div className="space-y-2">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        className="h-auto w-full rounded-xl border border-border bg-card"
        data-testid="entity-graph"
        role="img"
        aria-label="Entity knowledge graph"
        onClick={() => onSelect(null)}
      >
        {edges.map((e) => {
          const a = positions.get(e.src)
          const b = positions.get(e.dst)
          if (!a || !b) return null
          const incident = focus !== null && (e.src === focus || e.dst === focus)
          return (
            <line
              key={`${e.src}-${e.dst}`}
              x1={a.x}
              y1={a.y}
              x2={b.x}
              y2={b.y}
              strokeWidth={1 + 3 * (e.weight / maxWeight)}
              className={
                incident
                  ? 'stroke-brand'
                  : focus !== null
                    ? 'stroke-border opacity-30'
                    : 'stroke-border'
              }
            />
          )
        })}
        {data.entities.map((n) => {
          const p = positions.get(n.id)
          if (!p) return null
          const dimmed =
            focus !== null && focus !== n.id && !(neighbors.get(focus)?.has(n.id) ?? false)
          return (
            <g
              key={n.id}
              data-testid="entity-node"
              role="button"
              tabIndex={0}
              aria-label={n.name}
              className={`cursor-pointer outline-none ${dimmed ? 'opacity-40' : ''}`}
              onClick={(e) => {
                e.stopPropagation()
                onSelect(n.id === selectedId ? null : n.id)
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  onSelect(n.id === selectedId ? null : n.id)
                }
              }}
              onMouseEnter={() => setHoverId(n.id)}
              onMouseLeave={() => setHoverId(null)}
            >
              {n.id === selectedId && (
                <circle cx={p.x} cy={p.y} r={p.r + 4} className="fill-none stroke-brand" strokeWidth={2} />
              )}
              <circle cx={p.x} cy={p.y} r={p.r} className={kindFill[n.type] ?? 'fill-muted-foreground/60'} />
              <text
                x={p.x}
                y={p.y + p.r + 12}
                textAnchor="middle"
                className="fill-foreground text-[10px]"
              >
                {n.name.length > 18 ? `${n.name.slice(0, 17)}…` : n.name}
              </text>
              {n.memory_count > 0 && (
                <text
                  x={p.x}
                  y={p.y + p.r + 23}
                  textAnchor="middle"
                  className="fill-muted-foreground text-[9px]"
                >
                  {n.memory_count}
                </text>
              )}
            </g>
          )
        })}
      </svg>
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        {present.map((t) => (
          <span key={t} className="inline-flex items-center gap-1.5">
            <svg viewBox="0 0 8 8" className="size-2" aria-hidden="true">
              <circle cx="4" cy="4" r="4" className={kindFill[t] ?? 'fill-muted-foreground/60'} />
            </svg>
            {t}
          </span>
        ))}
      </div>
    </div>
  )
}
