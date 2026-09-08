import { act, renderHook } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { useStagedForm } from './useStagedForm'

describe('useStagedForm', () => {
  it('starts clean with the baseline values', () => {
    const { result } = renderHook(() => useStagedForm({ name: 'a', region: 'us-east-1' }))
    expect(result.current.values).toEqual({ name: 'a', region: 'us-east-1' })
    expect(result.current.dirty).toBe(false)
    expect(result.current.dirtyFields).toEqual([])
  })

  it('marks a field dirty when its value differs from the baseline', () => {
    const { result } = renderHook(() => useStagedForm({ name: 'a', region: 'us-east-1' }))
    act(() => result.current.setField('name', 'b'))
    expect(result.current.dirty).toBe(true)
    expect(result.current.dirtyFields).toEqual(['name'])
  })

  it('is not dirty when a touched field is set back to the baseline value', () => {
    const { result } = renderHook(() => useStagedForm({ name: 'a' }))
    act(() => result.current.setField('name', 'b'))
    act(() => result.current.setField('name', 'a'))
    expect(result.current.dirty).toBe(false)
  })

  it('reset restores the baseline and clears dirty', () => {
    const { result } = renderHook(() => useStagedForm({ name: 'a' }))
    act(() => result.current.setField('name', 'b'))
    act(() => result.current.reset())
    expect(result.current.values).toEqual({ name: 'a' })
    expect(result.current.dirty).toBe(false)
  })

  it('rebase applies the new baseline to untouched fields only', () => {
    const { result, rerender } = renderHook(
      ({ baseline }) => useStagedForm(baseline),
      { initialProps: { baseline: { name: 'a', region: 'us-east-1' } } },
    )
    act(() => result.current.setField('region', 'eu-west-1'))

    rerender({ baseline: { name: 'refetched', region: 'us-east-1' } })
    act(() => result.current.rebase({ name: 'refetched', region: 'us-east-1' }))

    expect(result.current.values.name).toBe('refetched')
    expect(result.current.values.region).toBe('eu-west-1')
  })

  it('deep-compares array and object fields for dirty', () => {
    const { result } = renderHook(() => useStagedForm({ chain: [{ id: 1 }] }))
    act(() => result.current.setField('chain', [{ id: 1 }]))
    expect(result.current.dirty).toBe(false)

    act(() => result.current.setField('chain', [{ id: 2 }]))
    expect(result.current.dirty).toBe(true)
  })

  it('is dirty when a field changes type from the baseline', () => {
    const { result } = renderHook(() => useStagedForm<{ v: string | number }>({ v: '1' }))
    act(() => result.current.setField('v', 1))
    expect(result.current.dirty).toBe(true)
  })

  it('is dirty when an object field gains or loses a key', () => {
    const { result } = renderHook(() => useStagedForm({ opts: { a: 1 } as Record<string, number> }))
    act(() => result.current.setField('opts', { a: 1, b: 2 }))
    expect(result.current.dirty).toBe(true)
  })
})
