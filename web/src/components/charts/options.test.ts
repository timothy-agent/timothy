import { describe, expect, it } from 'vitest'
import {
  areaLinesOption,
  donutOption,
  gaugeOption,
  latencyBarsOption,
  multiLineOption,
  requestsErrorsOption,
  stackedBarsOption,
} from './options'
import type { LatencyRow } from '../../api/types'

const colorOf = (g: string) => (g === 'openai' ? '#111' : g === 'anthropic' ? '#222' : '#333')
const xLabel = (v: string) => v
const money = (v: number) => `$${v}`

describe('stackedBarsOption', () => {
  const rows = [
    { bucket: 'a', openai: 1, anthropic: 2 },
    { bucket: 'b', openai: 3, anthropic: 4 },
  ]
  const groups = ['openai', 'anthropic']

  it('emits one bar series per visible group, stacked', () => {
    const opt = stackedBarsOption(rows, groups, new Set(), colorOf, xLabel, money)
    const series = opt.series as Array<{ type: string; stack: string; name: string; data: number[] }>
    expect(series).toHaveLength(2)
    expect(series.every((s) => s.type === 'bar' && s.stack === 'total')).toBe(true)
    expect(series[0].data).toEqual([1, 3])
    expect(series[1].data).toEqual([2, 4])
  })

  it('excludes hidden groups from series but keeps categories intact', () => {
    const opt = stackedBarsOption(rows, groups, new Set(['anthropic']), colorOf, xLabel, money)
    const series = opt.series as Array<{ name: string }>
    expect(series.map((s) => s.name)).toEqual(['openai'])
    expect((opt.xAxis as { data: string[] }).data).toEqual(['a', 'b'])
  })

  it('formats the y-axis and tooltip values through valueLabel', () => {
    const opt = stackedBarsOption(rows, groups, new Set(), colorOf, xLabel, money)
    const yAxis = opt.yAxis as { axisLabel: { formatter: (v: number) => string } }
    expect(yAxis.axisLabel.formatter(5)).toBe('$5')
  })

  it('includes inside dataZoom only', () => {
    const opt = stackedBarsOption(rows, groups, new Set(), colorOf, xLabel, money)
    const zoom = opt.dataZoom as Array<{ type: string }>
    expect(zoom.map((z) => z.type)).toEqual(['inside'])
  })
})

describe('areaLinesOption / multiLineOption', () => {
  const rows = [
    { bucket: 'a', input: 10, output: 5 },
    { bucket: 'b', input: 20, output: 8 },
  ]
  const groups = ['input', 'output']

  it('areaLinesOption sets areaStyle on every series', () => {
    const opt = areaLinesOption(rows, groups, new Set(), colorOf, xLabel, money)
    const series = opt.series as Array<{ areaStyle?: unknown }>
    expect(series.every((s) => s.areaStyle != null)) .toBe(true)
  })

  it('multiLineOption strips areaStyle', () => {
    const opt = multiLineOption(rows, groups, new Set(), colorOf, xLabel, money)
    const series = opt.series as Array<{ areaStyle?: unknown }>
    expect(series.every((s) => s.areaStyle == null)).toBe(true)
  })

  it('hides a hidden group entirely', () => {
    const opt = multiLineOption(rows, groups, new Set(['output']), colorOf, xLabel, money)
    const series = opt.series as Array<{ name: string }>
    expect(series.map((s) => s.name)).toEqual(['input'])
  })
})

describe('requestsErrorsOption', () => {
  it('builds two fixed series: requests bars and error-rate line', () => {
    const rows = [
      { bucket: 'a', requests: 10, errors: 1 },
      { bucket: 'b', requests: 0, errors: 0 },
    ]
    const opt = requestsErrorsOption(rows, xLabel, '#111', '#222')
    const series = opt.series as Array<{ name: string; type: string; data: number[] }>
    expect(series).toHaveLength(2)
    expect(series[0]).toMatchObject({ name: 'requests', type: 'bar', data: [10, 0] })
    expect(series[1].name).toBe('error rate')
    expect(series[1].type).toBe('line')
    // 1/10 = 10%; the zero-request bucket must not divide by zero.
    expect(series[1].data).toEqual([10, 0])
  })

  it('uses a second y-axis for error rate', () => {
    const opt = requestsErrorsOption([{ bucket: 'a', requests: 1, errors: 0 }], xLabel, '#111', '#222')
    expect(Array.isArray(opt.yAxis)).toBe(true)
    expect((opt.yAxis as unknown[]).length).toBe(2)
  })
})

describe('donutOption', () => {
  it('includes one slice per row when eight or fewer', () => {
    const rows = [
      { group: 'openai', cost: 10 },
      { group: 'anthropic', cost: 5 },
    ]
    const opt = donutOption(rows, colorOf, money)
    const series = opt.series as Array<{ data: Array<{ name: string; value: number }> }>
    expect(series[0].data).toHaveLength(2)
    expect(series[0].data.map((d) => d.name)).toEqual(['openai', 'anthropic'])
  })

  it('folds overflow past 8 slices into "other"', () => {
    const rows = Array.from({ length: 10 }, (_, i) => ({ group: `p${i}`, cost: 10 - i }))
    const opt = donutOption(rows, colorOf, money)
    const series = opt.series as Array<{ data: Array<{ name: string; value: number }> }>
    expect(series[0].data).toHaveLength(9)
    const other = series[0].data.find((d) => d.name === 'other')
    // p8 (cost 2) + p9 (cost 1) folded together.
    expect(other?.value).toBe(3)
  })

  it('is a plain pie (no hole) with outside labels naming each slice', () => {
    const rows = [
      { group: 'openai', cost: 10 },
      { group: 'anthropic', cost: 5 },
    ]
    const opt = donutOption(rows, colorOf, money)
    const series = opt.series as Array<{ radius: string; label: { show: boolean; formatter: string } }>
    expect(series[0].radius).toBe('65%')
    expect(series[0].label.show).toBe(true)
    expect(series[0].label.formatter).toContain('{b}')
    expect(series[0].label.formatter).toContain('{d}%')
  })

  it('centers the pie in the panel', () => {
    const rows = [{ group: 'openai', cost: 10 }]
    const opt = donutOption(rows, colorOf, money)
    const series = opt.series as Array<{ center: [string, string] }>
    expect(series[0].center).toEqual(['50%', '50%'])
  })
})

describe('latencyBarsOption', () => {
  it('builds three series (p50/p95/p99) with one bar per provider', () => {
    const rows: LatencyRow[] = [
      { provider: 'openai', p50_ms: 100, p95_ms: 200, p99_ms: 300, requests: 5 },
      { provider: 'anthropic', p50_ms: 50, p95_ms: 90, p99_ms: 120, requests: 3 },
    ]
    const opt = latencyBarsOption(rows, { p50: '#1', p95: '#2', p99: '#3' }, (ms) => `${ms}ms`)
    const series = opt.series as Array<{ name: string; data: number[] }>
    expect(series.map((s) => s.name)).toEqual(['p50', 'p95', 'p99'])
    expect(series[0].data).toEqual([100, 50])
    expect((opt.yAxis as { data: string[] }).data).toEqual(['openai', 'anthropic'])
  })
})

describe('gaugeOption', () => {
  it('uses the normal color under budget', () => {
    const opt = gaugeOption(5, 10, '#0f0', '#f00', (v) => `$${v}`)
    const series = opt.series as Array<{ progress: { itemStyle: { color: string } } }>
    expect(series[0].progress.itemStyle.color).toBe('#0f0')
  })

  it('switches to the over-budget color when spend exceeds the limit', () => {
    const opt = gaugeOption(12, 10, '#0f0', '#f00', (v) => `$${v}`)
    const series = opt.series as Array<{ progress: { itemStyle: { color: string } } }>
    expect(series[0].progress.itemStyle.color).toBe('#f00')
  })

  it('formats spend through moneyLabel', () => {
    const opt = gaugeOption(5, 10, '#0f0', '#f00', (v) => `$${v}`)
    const series = opt.series as Array<{ detail: { formatter: () => string } }>
    expect(series[0].detail.formatter()).toBe('$5')
  })
})
