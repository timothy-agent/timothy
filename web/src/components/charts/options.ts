import type { EChartsOption } from 'echarts'
import type { LatencyRow } from '../../api/types'
import { buildBaseTheme } from './theme'

export type Row = Record<string, number | string>

// visibleGroups filters a full group list down to what StatsLegend
// left checked — the option builders never see hidden series at all,
// so ECharts' own legend/tooltip machinery never has to know about them.
function visibleGroups(groups: string[], hidden: Set<string>): string[] {
  return groups.filter((g) => !hidden.has(g))
}

// stackedBarsOption builds a stacked-bar-over-time panel (spend by
// provider/model): axisPointer cross tooltip, sorted desc by value,
// dataZoom inside (wheel/drag) only.
export function stackedBarsOption(
  rows: Row[],
  groups: string[],
  hidden: Set<string>,
  colorOf: (g: string) => string,
  xLabel: (v: string) => string,
  valueLabel: (v: number) => string,
): EChartsOption {
  const theme = buildBaseTheme()
  const vis = visibleGroups(groups, hidden)
  const categories = rows.map((r) => xLabel(String(r.bucket)))

  return {
    textStyle: theme.textStyle,
    grid: { left: 56, right: 16, top: 16, bottom: 56, containLabel: true },
    xAxis: {
      type: 'category',
      data: categories,
      axisLine: theme.axisLine,
      axisLabel: theme.axisLabel,
      axisTick: theme.axisTick,
    },
    yAxis: {
      type: 'value',
      axisLabel: { ...theme.axisLabel, formatter: (v: number) => valueLabel(v) },
      splitLine: theme.splitLine,
    },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'cross' },
      ...theme.tooltip,
      order: 'valueDesc',
      valueFormatter: (v) => valueLabel(Number(v)),
    },
    dataZoom: [{ type: 'inside' }],
    series: vis.map((g) => ({
      name: g,
      type: 'bar',
      stack: 'total',
      barMaxWidth: 24,
      itemStyle: { color: colorOf(g), borderRadius: [2, 2, 0, 0] },
      data: rows.map((r) => Number(r[g]) || 0),
    })),
  }
}

// areaLinesOption builds a smooth stacked-area panel (tokens in/out).
export function areaLinesOption(
  rows: Row[],
  groups: string[],
  hidden: Set<string>,
  colorOf: (g: string) => string,
  xLabel: (v: string) => string,
  valueLabel: (v: number) => string,
): EChartsOption {
  const theme = buildBaseTheme()
  const vis = visibleGroups(groups, hidden)
  const categories = rows.map((r) => xLabel(String(r.bucket)))

  return {
    textStyle: theme.textStyle,
    grid: { left: 56, right: 16, top: 16, bottom: 32, containLabel: true },
    xAxis: {
      type: 'category',
      data: categories,
      axisLine: theme.axisLine,
      axisLabel: theme.axisLabel,
      axisTick: theme.axisTick,
    },
    yAxis: {
      type: 'value',
      axisLabel: { ...theme.axisLabel, formatter: (v: number) => valueLabel(v) },
      splitLine: theme.splitLine,
    },
    tooltip: {
      trigger: 'axis',
      ...theme.tooltip,
      order: 'valueDesc',
      valueFormatter: (v) => valueLabel(Number(v)),
    },
    series: vis.map((g) => ({
      name: g,
      type: 'line',
      smooth: true,
      symbol: 'circle',
      symbolSize: 6,
      showSymbol: false,
      areaStyle: { opacity: 0.15 },
      lineStyle: { width: 2, color: colorOf(g) },
      itemStyle: { color: colorOf(g) },
      data: rows.map((r) => Number(r[g]) || 0),
    })),
  }
}

// multiLineOption builds a plain (non-area) multi-line panel (tokens
// per model).
export function multiLineOption(
  rows: Row[],
  groups: string[],
  hidden: Set<string>,
  colorOf: (g: string) => string,
  xLabel: (v: string) => string,
  valueLabel: (v: number) => string,
): EChartsOption {
  const option = areaLinesOption(rows, groups, hidden, colorOf, xLabel, valueLabel)
  const series = Array.isArray(option.series) ? option.series : []
  return { ...option, series: series.map((s) => ({ ...s, areaStyle: undefined })) }
}

// requestsErrorsOption is the fixed two-series dual-axis panel:
// requests as bars (left axis), error rate % as a line (right axis).
export function requestsErrorsOption(
  rows: { bucket: string; requests: number; errors: number }[],
  xLabel: (v: string) => string,
  requestColor: string,
  errorColor: string,
): EChartsOption {
  const theme = buildBaseTheme()
  const categories = rows.map((r) => xLabel(r.bucket))
  const errorRate = rows.map((r) => (r.requests > 0 ? (r.errors / r.requests) * 100 : 0))

  return {
    textStyle: theme.textStyle,
    grid: { left: 48, right: 48, top: 16, bottom: 32, containLabel: true },
    xAxis: {
      type: 'category',
      data: categories,
      axisLine: theme.axisLine,
      axisLabel: theme.axisLabel,
      axisTick: theme.axisTick,
    },
    // Axis names are omitted — the legend already names each series,
    // and a name label here clips against the panel's top edge.
    yAxis: [
      { type: 'value', axisLabel: theme.axisLabel, splitLine: theme.splitLine },
      {
        type: 'value',
        axisLabel: { ...theme.axisLabel, formatter: '{value}%' },
        splitLine: { show: false },
      },
    ],
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'cross' },
      ...theme.tooltip,
    },
    legend: { textStyle: theme.textStyle, top: 0 },
    series: [
      {
        name: 'requests',
        type: 'bar',
        barMaxWidth: 24,
        itemStyle: { color: requestColor },
        data: rows.map((r) => r.requests),
      },
      {
        name: 'error rate',
        type: 'line',
        yAxisIndex: 1,
        smooth: true,
        symbol: 'circle',
        symbolSize: 6,
        lineStyle: { width: 2, color: errorColor },
        itemStyle: { color: errorColor },
        data: errorRate.map((v) => Number(v.toFixed(1))),
      },
    ],
  }
}

// donutOption builds a spend-share pie: provider % of billed cost,
// slice labels (name + percent) outside with label lines. Caps at 8
// slices, folding overflow into "other". Plain pie (no hole), centered
// in the panel — no legend needed since every slice is direct-labeled.
// A row may carry its own tooltip `label` (a slice whose amount stayed
// in a foreign currency shows that currency, not the chart's); rows
// without one, and the "other" bucket, fall back to valueLabel.
export function donutOption(
  rows: { group: string; cost: number; label?: string }[],
  colorOf: (g: string) => string,
  valueLabel: (v: number) => string,
): EChartsOption {
  const theme = buildBaseTheme()
  const sorted = [...rows].sort((a, b) => b.cost - a.cost)
  const top = sorted.slice(0, 8)
  const rest = sorted.slice(8)
  const restTotal = rest.reduce((n, r) => n + r.cost, 0)
  const labels = new Map(top.filter((r) => r.label).map((r) => [r.group, r.label as string]))
  const data = top.map((r) => ({ name: r.group, value: r.cost, itemStyle: { color: colorOf(r.group) } }))
  if (restTotal > 0) data.push({ name: 'other', value: restTotal, itemStyle: { color: 'var(--muted-foreground)' } })

  return {
    textStyle: theme.textStyle,
    tooltip: {
      trigger: 'item',
      ...theme.tooltip,
      formatter: (p) => {
        const param = p as { name: string; value: number; percent: number }
        const label = labels.get(param.name) ?? valueLabel(param.value)
        return `${param.name}<br/>${label} (${param.percent}%)`
      },
    },
    series: [
      {
        type: 'pie',
        center: ['50%', '50%'],
        radius: '65%',
        avoidLabelOverlap: true,
        label: {
          show: true,
          formatter: '{b}\n{d}%',
          color: theme.textStyle.color,
          fontFamily: theme.textStyle.fontFamily,
          fontSize: 11,
        },
        labelLine: { show: true, lineStyle: { color: theme.axisLine.lineStyle.color } },
        data,
      },
    ],
  }
}

// latencyBarsOption builds horizontal grouped bars (p50/p95/p99 per
// provider).
export function latencyBarsOption(
  rows: LatencyRow[],
  colors: { p50: string; p95: string; p99: string },
  formatDuration: (ms: number) => string,
): EChartsOption {
  const theme = buildBaseTheme()
  const providers = rows.map((r) => r.provider)

  return {
    textStyle: theme.textStyle,
    grid: { left: 8, right: 24, top: 16, bottom: 24, containLabel: true },
    xAxis: {
      type: 'value',
      axisLabel: { ...theme.axisLabel, formatter: (v: number) => formatDuration(v) },
      splitLine: theme.splitLine,
    },
    yAxis: {
      type: 'category',
      data: providers,
      axisLine: theme.axisLine,
      axisLabel: theme.axisLabel,
      axisTick: { show: false },
    },
    legend: { data: ['p50', 'p95', 'p99'], textStyle: theme.textStyle, top: 0 },
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
      ...theme.tooltip,
      valueFormatter: (v) => formatDuration(Number(v)),
    },
    series: [
      { name: 'p50', type: 'bar', itemStyle: { color: colors.p50 }, data: rows.map((r) => r.p50_ms) },
      { name: 'p95', type: 'bar', itemStyle: { color: colors.p95 }, data: rows.map((r) => r.p95_ms) },
      { name: 'p99', type: 'bar', itemStyle: { color: colors.p99 }, data: rows.map((r) => r.p99_ms) },
    ],
  }
}

// gaugeOption builds one budget gauge: progress arc, spend vs limit,
// over-budget rendered in the given overColor (destructive/amber tone).
export function gaugeOption(
  spend: number,
  limit: number,
  color: string,
  overColor: string,
  moneyLabel: (v: number) => string,
): EChartsOption {
  const theme = buildBaseTheme()
  const over = spend > limit
  const max = Math.max(limit, spend)

  return {
    textStyle: theme.textStyle,
    series: [
      {
        type: 'gauge',
        startAngle: 210,
        endAngle: -30,
        min: 0,
        max,
        progress: { show: true, width: 14, itemStyle: { color: over ? overColor : color } },
        axisLine: { lineStyle: { width: 14, color: [[1, 'var(--border)']] } },
        axisTick: { show: false },
        splitLine: { show: false },
        axisLabel: { show: false },
        pointer: { show: false },
        anchor: { show: false },
        detail: {
          valueAnimation: true,
          formatter: () => moneyLabel(spend),
          color: over ? overColor : theme.textStyle.color,
          fontSize: 16,
          fontWeight: 600,
          offsetCenter: [0, '20%'],
        },
        title: {
          show: true,
          offsetCenter: [0, '55%'],
          fontSize: 11,
          color: 'var(--muted-foreground)',
        },
        data: [{ value: spend, name: `of ${moneyLabel(limit)}` }],
      },
    ],
  }
}
