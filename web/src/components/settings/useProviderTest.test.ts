import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { useProviderTest } from './useProviderTest'

vi.mock('../../api/client', () => ({
  testProvider: vi.fn(),
}))

import { testProvider } from '../../api/client'

describe('useProviderTest', () => {
  it('starts idle', () => {
    const { result } = renderHook(() => useProviderTest('p1'))
    expect(result.current.state).toBe('idle')
  })

  it('renders success with model, latency and the responses-api suffix', async () => {
    vi.mocked(testProvider).mockResolvedValue({
      ok: true,
      latency_ms: 42,
      model: 'gpt-4o',
      responses_ok: true,
    })
    const { result } = renderHook(() => useProviderTest('p1'))

    await act(() => result.current.run())

    expect(result.current.state).toBe('ok')
    expect(result.current.message).toMatch(/^OK, gpt-4o answered in 42 ms\./)
  })

  it('renders a failure with the humanized probe detail', async () => {
    vi.mocked(testProvider).mockResolvedValue({
      ok: false,
      latency_ms: 10,
      model: 'gpt-4o',
      detail: 'connection refused',
    })
    const { result } = renderHook(() => useProviderTest('p1'))

    await act(() => result.current.run())

    expect(result.current.state).toBe('failed')
    expect(result.current.message).toContain('Failed after 10 ms')
    expect(result.current.detail).toBe('connection refused')
  })

  it('stays idle (never paints a failed probe) on a Timothy auth failure detail', async () => {
    vi.mocked(testProvider).mockResolvedValue({
      ok: false,
      latency_ms: 0,
      model: 'gpt-4o',
      detail: 'missing or invalid bearer token',
    })
    const { result } = renderHook(() => useProviderTest('p1'))

    await act(() => result.current.run())

    expect(result.current.state).toBe('idle')
  })

  it('stays idle when testProvider throws a Timothy auth error', async () => {
    vi.mocked(testProvider).mockRejectedValue({ status: 401, message: 'missing or invalid bearer token' })
    const { result } = renderHook(() => useProviderTest('p1'))

    await act(() => result.current.run())

    expect(result.current.state).toBe('idle')
  })

  it('renders failed with the thrown error message when testProvider throws a plain error', async () => {
    vi.mocked(testProvider).mockRejectedValue(new Error('network down'))
    const { result } = renderHook(() => useProviderTest('p1'))

    await act(() => result.current.run())

    expect(result.current.state).toBe('failed')
    expect(result.current.detail).toBe('network down')
  })

  it('renders failed with a stringified value when testProvider throws a non-Error', async () => {
    vi.mocked(testProvider).mockRejectedValue('boom')
    const { result } = renderHook(() => useProviderTest('p1'))

    await act(() => result.current.run())

    expect(result.current.state).toBe('failed')
    expect(result.current.detail).toBe('boom')
  })

  it('sets testing state immediately when run is called', async () => {
    let resolve!: (v: unknown) => void
    vi.mocked(testProvider).mockReturnValue(new Promise((r) => (resolve = r)))
    const { result } = renderHook(() => useProviderTest('p1'))

    act(() => {
      void result.current.run()
    })
    expect(result.current.state).toBe('testing')

    resolve({ ok: true, latency_ms: 5, model: 'm' })
    await waitFor(() => expect(result.current.state).toBe('ok'))
  })
})
