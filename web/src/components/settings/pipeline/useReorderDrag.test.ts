import { describe, expect, it } from 'vitest'
import { reorder, targetIndex } from './useReorderDrag'

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
