import { useRef, useState } from 'react'
import { niceMax } from './palette'
import { ChartTooltip, type TooltipState } from './ChartTooltip'
import type { LatencyRow } from '../../api/types'

const pctColors = { p50_ms: '#2a78d6', p95_ms: '#eda100', p99_ms: '#e34948' } as const
const pctLabel = { p50_ms: 'p50', p95_ms: 'p95', p99_ms: 'p99' } as const

export function LatencyBars({ rows }: { rows: LatencyRow[] }) {
  const [tooltip, setTooltip] = useState<TooltipState | null>(null)
  const svgRef = useRef<SVGSVGElement>(null)

  const rowH = 46
  const W = 640
  const H = rows.length * rowH + 16
  const padL = 84
  const padR = 40
  const plotW = W - padL - padR
  const maxV = niceMax(Math.max(...rows.map((r) => r.p99_ms), 1) * 1.1)

  function pointerLabel(e: React.PointerEvent, r: LatencyRow, key: keyof typeof pctColors) {
    const svg = svgRef.current
    if (!svg) return
    const rect = svg.getBoundingClientRect()
    setTooltip({
      x: e.clientX - rect.left,
      y: e.clientY - rect.top,
      label: `${r.provider} · ${pctLabel[key]}`,
      rows: [{ key: pctLabel[key], color: pctColors[key], value: `${Math.round(r[key])} ms` }],
    })
  }

  return (
    <div className="relative">
      <svg ref={svgRef} viewBox={`0 0 ${W} ${H}`} className="h-auto w-full" onPointerLeave={() => setTooltip(null)}>
        {rows.map((r, i) => {
          const cy = 10 + i * rowH
          return (
            <g key={r.provider}>
              <text x={padL - 10} y={cy + 22} textAnchor="end" fontSize={12} fill="currentColor" opacity={0.75}>
                {r.provider}
              </text>
              {(['p50_ms', 'p95_ms', 'p99_ms'] as const).map((key, j) => {
                const v = r[key]
                const w = (v / maxV) * plotW
                const y = cy + j * 9
                return (
                  <rect
                    key={key}
                    x={padL}
                    y={y}
                    width={w}
                    height={6}
                    rx={3}
                    fill={pctColors[key]}
                    opacity={key === 'p50_ms' ? 1 : key === 'p95_ms' ? 0.75 : 0.5}
                    onPointerMove={(e) => pointerLabel(e, r, key)}
                  />
                )
              })}
            </g>
          )
        })}
      </svg>
      <ChartTooltip state={tooltip} />
      <div className="mt-1 flex gap-3 text-[11px] text-muted-foreground">
        {(['p50_ms', 'p95_ms', 'p99_ms'] as const).map((key) => (
          <span key={key} className="flex items-center gap-1">
            <span className="h-2 w-2 rounded-[2px]" style={{ background: pctColors[key] }} />
            {pctLabel[key]}
          </span>
        ))}
      </div>
    </div>
  )
}
