import { useRef, useState } from 'react'
import { niceMax } from './palette'
import { ChartTooltip, type TooltipState } from './ChartTooltip'

export interface MultiLineChartProps {
  rows: Record<string, number | string>[]
  groups: string[]
  colorOf: (g: string) => string
  hidden: Set<string>
  xKey?: string
  xLabel: (v: string) => string
  valueLabel: (v: number) => string
  height?: number
}

// 2px lines, round joins, ≥8px end markers with a surface ring, hairline
// gridlines, and a hover crosshair with a synced tooltip.
export function MultiLineChart({
  rows,
  groups,
  colorOf,
  hidden,
  xKey = 'bucket',
  xLabel,
  valueLabel,
  height = 240,
}: MultiLineChartProps) {
  const [tooltip, setTooltip] = useState<TooltipState | null>(null)
  const [hoverX, setHoverX] = useState<number | null>(null)
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
  const allVals = visibleGroups.flatMap((g) => rows.map((r) => Number(r[g]) || 0))
  const maxV = niceMax(Math.max(...allVals, 1) * 1.15)
  const ticks = 4
  const stepX = rows.length > 1 ? plotW / (rows.length - 1) : plotW

  function yOf(v: number) {
    return padT + plotH - (v / maxV) * plotH
  }

  function pointerLabel(e: React.PointerEvent, i: number) {
    const svg = svgRef.current
    if (!svg) return
    const rect = svg.getBoundingClientRect()
    const row = rows[i]
    setHoverX(padL + stepX * i)
    setTooltip({
      x: e.clientX - rect.left,
      y: e.clientY - rect.top,
      label: xLabel(String(row[xKey])),
      rows: visibleGroups.map((g) => ({ key: g, color: colorOf(g), value: valueLabel(Number(row[g]) || 0) })),
    })
  }

  return (
    <div className="relative">
      <svg
        ref={svgRef}
        viewBox={`0 0 ${W} ${H}`}
        className="h-auto w-full"
        onPointerLeave={() => {
          setTooltip(null)
          setHoverX(null)
        }}
      >
        {Array.from({ length: ticks + 1 }, (_, t) => {
          const v = (maxV / ticks) * t
          const y = yOf(v)
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
        {rows.map((row, i) => (
          <text
            key={String(row[xKey])}
            x={padL + stepX * i}
            y={H - 6}
            textAnchor="middle"
            fontSize={10.5}
            fill="currentColor"
            opacity={0.55}
          >
            {xLabel(String(row[xKey]))}
          </text>
        ))}
        {hoverX != null && (
          <line x1={hoverX} x2={hoverX} y1={padT} y2={padT + plotH} stroke="currentColor" strokeOpacity={0.15} />
        )}
        {visibleGroups.map((g) => {
          const points = rows.map((r, i) => `${padL + stepX * i},${yOf(Number(r[g]) || 0)}`).join(' ')
          const last = rows[rows.length - 1]
          const lastXY = last ? [padL + stepX * (rows.length - 1), yOf(Number(last[g]) || 0)] : null
          return (
            <g key={g}>
              <polyline
                points={points}
                fill="none"
                stroke={colorOf(g)}
                strokeWidth={2}
                strokeLinejoin="round"
                strokeLinecap="round"
              />
              {lastXY && (
                <circle
                  cx={lastXY[0]}
                  cy={lastXY[1]}
                  r={4}
                  fill={colorOf(g)}
                  stroke="var(--background, #fff)"
                  strokeWidth={2}
                />
              )}
            </g>
          )
        })}
        {rows.map((row, i) => (
          <rect
            key={String(row[xKey])}
            x={padL + stepX * i - stepX / 2}
            y={padT}
            width={stepX}
            height={plotH}
            fill="transparent"
            onPointerMove={(e) => pointerLabel(e, i)}
          />
        ))}
      </svg>
      <ChartTooltip state={tooltip} />
    </div>
  )
}
