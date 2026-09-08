import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { EntityGraphData } from '../../api/types'
import { EntityGraph } from './EntityGraph'

// jsdom has no canvas: EChart inits a real chart on mount, which throws
// without a canvas 2d context. Stub the tree-shaken core entry point
// with a no-op instance that records registered handlers so tests can
// drive node/blank clicks directly, matching GraphTab.test.tsx's mock.
let clickHandler: ((params: unknown) => void) | null = null
let zrMousedownHandler: ((event: unknown) => void) | null = null
let zrClickHandler: ((event: unknown) => void) | null = null
const dispatchAction = vi.fn()

vi.mock('echarts/core', () => ({
  use: vi.fn(),
  init: vi.fn(() => ({
    setOption: vi.fn(),
    resize: vi.fn(),
    dispose: vi.fn(),
    dispatchAction,
    on: vi.fn((_event: string, _opts: unknown, handler: (params: unknown) => void) => {
      clickHandler = handler
    }),
    getZr: vi.fn(() => ({
      on: vi.fn((event: string, handler: (event: unknown) => void) => {
        if (event === 'mousedown') zrMousedownHandler = handler
        if (event === 'click') zrClickHandler = handler
      }),
    })),
  })),
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

const graph: EntityGraphData = {
  entities: [
    { id: 'e1', type: 'project', name: 'timothy', memory_count: 2 },
    { id: 'e2', type: 'person', name: 'sumon', memory_count: 1 },
  ],
  edges: [{ src: 'e1', dst: 'e2', weight: 1 }],
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  clickHandler = null
  zrMousedownHandler = null
  zrClickHandler = null
})

describe('EntityGraph selection effect', () => {
  it('unselects only when selectedId is null', async () => {
    render(<EntityGraph data={graph} selectedId={null} onSelect={vi.fn()} />)
    await screen.findByTestId('entity-graph')
    await waitFor(() => expect(dispatchAction).toHaveBeenCalledWith({ type: 'unselect', seriesId: 'entities' }))
    expect(dispatchAction).not.toHaveBeenCalledWith(expect.objectContaining({ type: 'select' }))
  })

  it('selects the node at its index when selectedId matches a visible node', async () => {
    render(<EntityGraph data={graph} selectedId="e2" onSelect={vi.fn()} />)
    await screen.findByTestId('entity-graph')
    await waitFor(() =>
      expect(dispatchAction).toHaveBeenLastCalledWith({ type: 'select', seriesId: 'entities', dataIndex: 1 }),
    )
  })

  it('unselects only, without a select call, when selectedId is hidden by a kind toggle', async () => {
    const { rerender } = render(<EntityGraph data={graph} selectedId="e2" onSelect={vi.fn()} />)
    await screen.findByTestId('entity-graph')
    await waitFor(() =>
      expect(dispatchAction).toHaveBeenLastCalledWith({ type: 'select', seriesId: 'entities', dataIndex: 1 }),
    )
    dispatchAction.mockClear()
    fireEvent.click(screen.getByRole('button', { name: 'person' }))
    rerender(<EntityGraph data={graph} selectedId="e2" onSelect={vi.fn()} />)
    await waitFor(() => expect(dispatchAction).toHaveBeenCalledWith({ type: 'unselect', seriesId: 'entities' }))
    expect(dispatchAction).not.toHaveBeenCalledWith(expect.objectContaining({ type: 'select' }))
  })
})

describe('EntityGraph background click', () => {
  it('does nothing when the click landed on an element (has a target)', async () => {
    const onSelect = vi.fn()
    render(<EntityGraph data={graph} selectedId="e1" onSelect={onSelect} />)
    await screen.findByTestId('entity-graph')
    zrMousedownHandler!({ offsetX: 100, offsetY: 100 })
    zrClickHandler!({ target: {}, offsetX: 100, offsetY: 100 })
    expect(onSelect).not.toHaveBeenCalled()
  })

  it('deselects on a background click with no prior mousedown', async () => {
    const onSelect = vi.fn()
    render(<EntityGraph data={graph} selectedId="e1" onSelect={onSelect} />)
    await screen.findByTestId('entity-graph')
    zrClickHandler!({ target: null, offsetX: 100, offsetY: 100 })
    expect(onSelect).toHaveBeenCalledWith(null)
  })

  it('falls back to 0,0 when mousedown is missing offsetX/offsetY, still counts as a click', async () => {
    const onSelect = vi.fn()
    render(<EntityGraph data={graph} selectedId="e1" onSelect={onSelect} />)
    await screen.findByTestId('entity-graph')
    zrMousedownHandler!({})
    zrClickHandler!({ target: null, offsetX: 0, offsetY: 0 })
    expect(onSelect).toHaveBeenCalledWith(null)
  })

  it('falls back to 0,0 when click is missing offsetX/offsetY', async () => {
    const onSelect = vi.fn()
    render(<EntityGraph data={graph} selectedId="e1" onSelect={onSelect} />)
    await screen.findByTestId('entity-graph')
    zrMousedownHandler!({ offsetX: 0, offsetY: 0 })
    zrClickHandler!({ target: null })
    expect(onSelect).toHaveBeenCalledWith(null)
  })

  it('keeps the selection when the pointer moved past the click threshold', async () => {
    const onSelect = vi.fn()
    render(<EntityGraph data={graph} selectedId="e1" onSelect={onSelect} />)
    await screen.findByTestId('entity-graph')
    zrMousedownHandler!({ offsetX: 100, offsetY: 100 })
    zrClickHandler!({ target: null, offsetX: 120, offsetY: 100 })
    expect(onSelect).not.toHaveBeenCalled()
  })
})

describe('EntityGraph node click', () => {
  it('selects a node when it is not already selected', async () => {
    const onSelect = vi.fn()
    render(<EntityGraph data={graph} selectedId={null} onSelect={onSelect} />)
    await screen.findByTestId('entity-graph')
    clickHandler!({ dataType: 'node', data: { id: 'e1' } })
    expect(onSelect).toHaveBeenCalledWith('e1')
  })

  it('deselects when clicking the already-selected node', async () => {
    const onSelect = vi.fn()
    render(<EntityGraph data={graph} selectedId="e1" onSelect={onSelect} />)
    await screen.findByTestId('entity-graph')
    clickHandler!({ dataType: 'node', data: { id: 'e1' } })
    expect(onSelect).toHaveBeenCalledWith(null)
  })

  it('ignores clicks that are not on a node', async () => {
    const onSelect = vi.fn()
    render(<EntityGraph data={graph} selectedId={null} onSelect={onSelect} />)
    await screen.findByTestId('entity-graph')
    clickHandler!({ dataType: 'edge', data: { id: 'e1' } })
    expect(onSelect).not.toHaveBeenCalled()
  })

  it('ignores a node click with no id', async () => {
    const onSelect = vi.fn()
    render(<EntityGraph data={graph} selectedId={null} onSelect={onSelect} />)
    await screen.findByTestId('entity-graph')
    clickHandler!({ dataType: 'node', data: {} })
    expect(onSelect).not.toHaveBeenCalled()
  })
})

describe('EntityGraph toggleKind', () => {
  it('hides a kind then shows it again', async () => {
    render(<EntityGraph data={graph} selectedId={null} onSelect={vi.fn()} />)
    await screen.findByTestId('entity-graph')
    const chip = screen.getByRole('button', { name: 'project' })
    expect(chip).not.toHaveClass('opacity-40')
    fireEvent.click(chip)
    expect(chip).toHaveClass('opacity-40')
    fireEvent.click(chip)
    expect(chip).not.toHaveClass('opacity-40')
  })

  it('toggles a kind via keyboard Enter/Space, ignoring other keys', async () => {
    render(<EntityGraph data={graph} selectedId={null} onSelect={vi.fn()} />)
    await screen.findByTestId('entity-graph')
    const chip = screen.getByRole('button', { name: 'project' })
    fireEvent.keyDown(chip, { key: 'a' })
    expect(chip).not.toHaveClass('opacity-40')
    fireEvent.keyDown(chip, { key: 'Enter' })
    expect(chip).toHaveClass('opacity-40')
    fireEvent.keyDown(chip, { key: ' ' })
    expect(chip).not.toHaveClass('opacity-40')
  })
})
