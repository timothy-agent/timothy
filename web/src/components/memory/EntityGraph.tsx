import { useEffect, useMemo, useRef, useState } from 'react'
import type * as echarts from 'echarts/core'
import type { EntityGraphData } from '../../api/types'
import { EChart } from '../charts/EChart'
import { colorOfKind, graphOption } from './graphOption'

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
  // Selection-only re-renders keep notMerge off so the force layout
  // doesn't restart; a data or filter change rebuilds it fully.
  const [selectMerge, setSelectMerge] = useState(false)
  // Click handlers are attached once (on chart init); read current
  // selection through a ref rather than closing over stale state.
  const selectedRef = useRef(selectedId)
  useEffect(() => {
    selectedRef.current = selectedId
  }, [selectedId])

  const option = useMemo(
    () => graphOption(data, hiddenKinds, selectedId),
    [data, hiddenKinds, selectedId],
  )

  const kinds = [...new Set(data.entities.map((n) => n.type))]

  function toggleKind(kind: string) {
    setSelectMerge(false)
    setHiddenKinds((prev) => {
      const next = new Set(prev)
      if (next.has(kind)) next.delete(kind)
      else next.add(kind)
      return next
    })
  }

  function select(id: string | null) {
    setSelectMerge(true)
    onSelect(id)
  }

  function attachHandlers(chart: echarts.ECharts) {
    chart.on('click', { seriesType: 'graph' }, (params) => {
      const p = params as { dataType?: string; data?: { id?: string } }
      if (p.dataType !== 'node' || !p.data?.id) return
      select(p.data.id === selectedRef.current ? null : p.data.id)
    })
    chart.getZr().on('click', (event) => {
      if (!event.target) select(null)
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
        <EChart option={option} height={520} notMerge={!selectMerge} onChartReady={attachHandlers} />
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
