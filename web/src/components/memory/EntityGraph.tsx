import { useEffect, useMemo, useRef, useState } from 'react'
import type * as echarts from 'echarts/core'
import type { EntityGraphData } from '../../api/types'
import { EChart } from '../charts/EChart'
import { colorOfKind, graphOption, visibleEntities } from './graphOption'

// EntityGraph renders the entity co-occurrence graph on ECharts' force
// layout: zoom/pan/drag, categories per entity kind, edges weighted by
// shared active memories. Click a node to inspect it, click the
// background to clear, click a legend chip to hide/show a kind.
export function EntityGraph({
  data,
  selectedId,
  onSelect,
}: {
  data: EntityGraphData
  selectedId: string | null
  onSelect: (id: string | null) => void
}) {
  const [hiddenKinds, setHiddenKinds] = useState<Set<string>>(new Set())
  // Click handlers are attached once (on chart init); read current
  // selection through a ref rather than closing over stale state.
  const selectedRef = useRef(selectedId)
  useEffect(() => {
    selectedRef.current = selectedId
  }, [selectedId])
  const chartRef = useRef<echarts.ECharts | null>(null)
  // Pointer-down position, to tell a pan release from a background
  // click: only a near-stationary pointer counts as a click-to-deselect.
  const downPosRef = useRef<{ x: number; y: number } | null>(null)

  // The option no longer depends on selectedId, so it never rebuilds
  // (and never restarts the force simulation) on selection alone.
  const option = useMemo(() => graphOption(data, hiddenKinds), [data, hiddenKinds])

  const kinds = [...new Set(data.entities.map((n) => n.type))]

  useEffect(() => {
    const chart = chartRef.current
    if (!chart) return
    // Series data is the kind-filtered list, so index into that, not data.entities.
    const dataIndex = visibleEntities(data, hiddenKinds).findIndex((n) => n.id === selectedId)
    chart.dispatchAction({ type: 'unselect', seriesId: 'entities' })
    if (dataIndex >= 0) {
      chart.dispatchAction({ type: 'select', seriesId: 'entities', dataIndex })
    }
  }, [selectedId, data, hiddenKinds])

  function toggleKind(kind: string) {
    setHiddenKinds((prev) => {
      const next = new Set(prev)
      if (next.has(kind)) next.delete(kind)
      else next.add(kind)
      return next
    })
  }

  function attachHandlers(chart: echarts.ECharts) {
    chartRef.current = chart
    chart.on('click', { seriesType: 'graph' }, (params) => {
      const p = params as { dataType?: string; data?: { id?: string } }
      if (p.dataType !== 'node' || !p.data?.id) return
      onSelect(p.data.id === selectedRef.current ? null : p.data.id)
    })
    chart.getZr().on('mousedown', (event) => {
      const e = event as { offsetX?: number; offsetY?: number }
      downPosRef.current = { x: e.offsetX ?? 0, y: e.offsetY ?? 0 }
    })
    chart.getZr().on('click', (event) => {
      const e = event as { target?: unknown; offsetX?: number; offsetY?: number }
      if (e.target) return
      const down = downPosRef.current
      const moved = down
        ? Math.hypot((e.offsetX ?? 0) - down.x, (e.offsetY ?? 0) - down.y)
        : 0
      if (moved < 5) onSelect(null)
    })
  }

  if (data.entities.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        No entities yet, the graph builds as Timothy extracts memories.
      </p>
    )
  }

  return (
    <div className="space-y-2">
      <div
        className="rounded-xl border border-border bg-card"
        data-testid="entity-graph"
        role="img"
        aria-label="Entity knowledge graph"
      >
        <EChart option={option} height={520} notMerge={false} onChartReady={attachHandlers} />
      </div>
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
        {kinds.map((k) => {
          const hidden = hiddenKinds.has(k)
          return (
            <span
              key={k}
              role="button"
              tabIndex={0}
              aria-label={k}
              onClick={() => toggleKind(k)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  toggleKind(k)
                }
              }}
              className={`inline-flex cursor-pointer items-center gap-1.5 select-none ${hidden ? 'opacity-40' : ''}`}
            >
              <svg viewBox="0 0 8 8" className="size-2" aria-hidden="true">
                <circle cx="4" cy="4" r="4" fill={colorOfKind(k)} />
              </svg>
              <span style={{ textDecoration: hidden ? 'line-through' : 'none' }}>{k}</span>
            </span>
          )
        })}
      </div>
    </div>
  )
}
