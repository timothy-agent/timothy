import { useEffect, useRef } from 'react'
import * as echarts from 'echarts/core'
import { BarChart, GaugeChart, GraphChart, LineChart, PieChart } from 'echarts/charts'
import {
  DataZoomComponent,
  GridComponent,
  LegendComponent,
  MarkLineComponent,
  TitleComponent,
  TooltipComponent,
} from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { EChartsOption } from 'echarts'

echarts.use([
  BarChart,
  LineChart,
  PieChart,
  GaugeChart,
  GraphChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  TitleComponent,
  DataZoomComponent,
  MarkLineComponent,
  CanvasRenderer,
])

export interface EChartProps {
  option: EChartsOption
  height?: number
  // fill makes the container stretch to its parent's height (h-full)
  // instead of the fixed height prop — for a panel laid out as a flex
  // column where the chart should take up the remaining space (e.g.
  // paired half-width panels of uneven content height).
  fill?: boolean
  // Panels rebuild their whole option on every change by default. A
  // graph with a force layout restarts its simulation on notMerge, so
  // callers doing in-place styling updates (selection) pass false.
  notMerge?: boolean
  // Fires after every (re)init so a caller can wire chart/zrender
  // event listeners (e.g. the entity graph's click-to-select).
  onChartReady?: (chart: echarts.ECharts) => void
}

// Thin wrapper: inits once, replaces the option on change (notMerge by
// default — panels rebuild their whole option rather than patching),
// resizes with the container, and re-inits when the app theme flips
// (light/dark tokens are baked into option, not swappable in place).
export function EChart({ option, height = 240, fill = false, notMerge = true, onChartReady }: EChartProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const chartRef = useRef<echarts.ECharts | null>(null)

  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    const chart = echarts.init(el)
    chartRef.current = chart
    onChartReady?.(chart)

    const resizeObserver = new ResizeObserver(() => chart.resize())
    resizeObserver.observe(el)

    const themeObserver = new MutationObserver(() => {
      chart.dispose()
      chartRef.current = echarts.init(el)
      chartRef.current.setOption(option, { notMerge })
      onChartReady?.(chartRef.current)
    })
    themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })

    return () => {
      resizeObserver.disconnect()
      themeObserver.disconnect()
      chart.dispose()
      chartRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    chartRef.current?.setOption(option, { notMerge })
  }, [option, notMerge])

  return (
    <div
      ref={containerRef}
      className={fill ? 'h-full' : undefined}
      style={{ height: fill ? undefined : height, width: '100%' }}
    />
  )
}
