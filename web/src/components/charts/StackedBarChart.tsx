import { useRef, useState } from 'react'
import { niceMax } from './palette'
import { ChartTooltip, type TooltipState } from './ChartTooltip'

export interface StackedBarChartProps {
  rows: Record<string, number | string>[]
  groups: string[]
  colorOf: (g: string) => string
  hidden: Set<string>
  xKey?: string
  xLabel: (v: string) => string
  valueLabel: (v: number) => string
  height?: number
}

// Fixed-viewBox SVG stacked bar: 4px rounded data-ends, 2px surface gaps
// between segments, hairline gridlines. Scales via viewBox, not a
// measured-container library, so it renders correctly in jsdom too.
export function StackedBarChart({
  rows,
  groups,
  colorOf,
  hidden,
  xKey = 'bucket',
  xLabel,
  valueLabel,
  height = 240,
}: StackedBarChartProps) {
  const [tooltip, setTooltip] = useState<TooltipState | null>(null)
  const svgRef = useRef<SVGSVGElement>(null)

  const W = 640
  const H = height
  const padL = 52
  const padR = 8
  const padT = 8
  const padB = 24
  const plotW = W - padL - padR
  const plotH = H - padT - padB

  const visibleGroups = groups.filter((g) => !hidden.has(g))
  const totals = rows.map((r) => visibleGroups.reduce((s, g) => s + (Number(r[g]) || 0), 0))
  const maxV = niceMax(Math.max(...totals, 0.0001) * 1.15)

  const ticks = 4
  const slot = rows.length > 0 ? plotW / rows.length : plotW
  const barW = Math.min(24, slot * 0.5)
  const gap = 2

  function pointerLabel(e: React.PointerEvent, row: Record<string, number | string>) {
    const svg = svgRef.current
    if (!svg) return
    const rect = svg.getBoundingClientRect()
    setTooltip({
      x: e.clientX - rect.left,
      y: e.clientY - rect.top,
      label: xLabel(String(row[xKey])),
      rows: visibleGroups
        .filter((g) => Number(row[g]) > 0)
        .map((g) => ({ key: g, color: colorOf(g), value: valueLabel(Number(row[g]) || 0) })),
    })
  }

  return (
    <div className="relative">
      <svg ref={svgRef} viewBox={`0 0 ${W} ${H}`} className="h-auto w-full" onPointerLeave={() => setTooltip(null)}>
        {Array.from({ length: ticks + 1 }, (_, t) => {
          const v = (maxV / ticks) * t
          const y = padT + plotH - (v / maxV) * plotH
          return (
            <g key={t}>
              <line
                x1={padL}
                x2={W - padR}
                y1={y}
                y2={y}
                stroke={t === 0 ? 'var(--border)' : 'currentColor'}
                strokeOpacity={t === 0 ? 1 : 0.12}
                strokeWidth={1}
              />
              <text x={padL - 8} y={y + 3} textAnchor="end" fontSize={10.5} fill="currentColor" opacity={0.55}>
                {valueLabel(v)}
              </text>
            </g>
          )
        })}
        {rows.map((row, i) => {
          const x = padL + slot * i + (slot - barW) / 2
          let yCursor = padT + plotH
          return (
            <g key={String(row[xKey])} onPointerMove={(e) => pointerLabel(e, row)}>
              <rect x={x} y={padT} width={barW} height={plotH} fill="transparent" />
              {visibleGroups.map((g) => {
                const v = Number(row[g]) || 0
                if (v <= 0) return null
                const segH = (v / maxV) * plotH
                const y = yCursor - segH
                yCursor -= segH
                return (
                  <rect
                    key={g}
                    x={x}
                    y={y + gap / 2}
                    width={barW}
                    height={Math.max(segH - gap, 0)}
                    rx={4}
                    ry={4}
                    fill={colorOf(g)}
                  />
                )
              })}
              <text
                x={x + barW / 2}
                y={H - 6}
                textAnchor="middle"
                fontSize={10.5}
                fill="currentColor"
                opacity={0.55}
              >
                {xLabel(String(row[xKey]))}
              </text>
            </g>
          )
        })}
      </svg>
      <ChartTooltip state={tooltip} />
    </div>
  )
}
