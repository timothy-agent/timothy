import { act, cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { EChart } from './EChart'

// jsdom has no canvas: stub the tree-shaken core entry point the same
// way GraphTab.test.tsx does, plus dispatchAction (unused here but
// keeps the mock shape consistent) so init/dispose calls are visible.
const setOption = vi.fn()
const dispose = vi.fn()
const init = vi.fn(() => ({
  setOption,
  resize: vi.fn(),
  dispose,
  dispatchAction: vi.fn(),
  on: vi.fn(),
  getZr: vi.fn(() => ({ on: vi.fn() })),
}))

vi.mock('echarts/core', () => ({
  use: vi.fn(),
  init: (...args: unknown[]) => init(...(args as [])),
}))
vi.mock('echarts/charts', () => ({ BarChart: {}, LineChart: {}, PieChart: {}, GaugeChart: {}, GraphChart: {} }))
vi.mock('echarts/components', () => ({
  GridComponent: {},
  TooltipComponent: {},
  LegendComponent: {},
  TitleComponent: {},
  DataZoomComponent: {},
  MarkLineComponent: {},
}))
vi.mock('echarts/renderers', () => ({ CanvasRenderer: {} }))

afterEach(() => {
  cleanup()
  document.documentElement.className = ''
})
beforeEach(() => {
  vi.clearAllMocks()
})

describe('EChart theme observer', () => {
  it('does not dispose or re-init when the class changes without flipping dark', async () => {
    render(<EChart option={{}} />)
    expect(init).toHaveBeenCalledTimes(1)
    await act(async () => {
      document.documentElement.className = 'focus-visible'
    })
    expect(dispose).not.toHaveBeenCalled()
    expect(init).toHaveBeenCalledTimes(1)
  })

  it('disposes and re-inits, then sets option again, when dark flips', async () => {
    render(<EChart option={{}} />)
    expect(init).toHaveBeenCalledTimes(1)
    await act(async () => {
      document.documentElement.classList.add('dark')
    })
    await waitFor(() => expect(dispose).toHaveBeenCalledTimes(1))
    expect(init).toHaveBeenCalledTimes(2)
    expect(setOption).toHaveBeenCalledWith({}, { notMerge: true })
  })
})
