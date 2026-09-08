import { renderHook } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { reorder, targetIndex, useReorderDrag } from './useReorderDrag'

describe('targetIndex', () => {
  const midpoints = [50, 150, 250]

  it.each([
    [0, 0], // left of everything
    [49, 0],
    [51, 1], // past the first midpoint
    [151, 2],
    [400, 2], // clamped to the last index
  ])('pointer at %d → index %d', (x, want) => {
    expect(targetIndex(midpoints, x)).toBe(want)
  })

  it('returns 0 for an empty list', () => {
    expect(targetIndex([], 100)).toBe(0)
  })
})

describe('reorder', () => {
  it('moves an element forward', () => {
    expect(reorder(['a', 'b', 'c'], 0, 2)).toEqual(['b', 'c', 'a'])
  })
  it('moves an element backward', () => {
    expect(reorder(['a', 'b', 'c'], 2, 0)).toEqual(['c', 'a', 'b'])
  })
  it('does not mutate the input', () => {
    const input = ['a', 'b']
    reorder(input, 0, 1)
    expect(input).toEqual(['a', 'b'])
  })
})

describe('useReorderDrag', () => {
  it('calls onCommit exactly once with (from, over) on pointerup after activation', () => {
    const onCommit = vi.fn()
    const { result } = renderHook(() => useReorderDrag({ onCommit }))

    const els = [0, 1, 2].map((i) => {
      const el = document.createElement('div')
      el.getBoundingClientRect = () =>
        ({ left: i * 100, width: 100, right: i * 100 + 100, top: 0, bottom: 50, height: 50, x: i * 100, y: 0 }) as DOMRect
      result.current.setItemRef(i)(el)
      return el
    })

    result.current.handleProps(0).onPointerDown({
      button: 0,
      clientX: 10,
      target: els[0],
    } as unknown as React.PointerEvent<HTMLElement>)
    window.dispatchEvent(new PointerEvent('pointermove', { clientX: 180 }))
    window.dispatchEvent(new PointerEvent('pointerup'))

    expect(onCommit).toHaveBeenCalledTimes(1)
    expect(onCommit).toHaveBeenCalledWith(0, 2)
  })
})
