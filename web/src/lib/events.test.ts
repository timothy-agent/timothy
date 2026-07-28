import { afterEach, describe, expect, it, vi } from 'vitest'
import { subscribeEvents } from './events'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('subscribeEvents', () => {
  it('fires onReady for the ready event and onSignal for a signal event', async () => {
    vi.stubGlobal('localStorage', { getItem: () => 'tok', setItem: () => {} })
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          'event: ready\ndata: {"type":"ready"}\n\n' +
            'event: signal\ndata: {"type":"signal","kind":"mission","id":"m1"}\n\n',
        ),
      ),
    )

    const onSignal = vi.fn()
    const onReady = vi.fn()
    const unsubscribe = subscribeEvents(onSignal, onReady)

    await vi.waitFor(() => expect(onReady).toHaveBeenCalledTimes(1))
    await vi.waitFor(() => expect(onSignal).toHaveBeenCalledWith({ kind: 'mission', id: 'm1' }))

    unsubscribe()
  })

  it('sends the bearer token from localStorage', async () => {
    vi.stubGlobal('localStorage', { getItem: () => 'tok', setItem: () => {} })
    const fetchMock = vi.fn().mockResolvedValue(new Response('event: ready\ndata: {"type":"ready"}\n\n'))
    vi.stubGlobal('fetch', fetchMock)

    const unsubscribe = subscribeEvents(vi.fn())
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled())

    expect(fetchMock.mock.calls[0][0]).toBe('/v1/events')
    const init = fetchMock.mock.calls[0][1] as RequestInit
    expect((init.headers as Record<string, string>).Authorization).toBe('Bearer tok')

    unsubscribe()
  })

  it('stops reconnecting once unsubscribed', async () => {
    vi.stubGlobal('localStorage', { getItem: () => 'tok', setItem: () => {} })
    const fetchMock = vi.fn().mockResolvedValue(new Response('event: ready\ndata: {"type":"ready"}\n\n'))
    vi.stubGlobal('fetch', fetchMock)

    const unsubscribe = subscribeEvents(vi.fn())
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    unsubscribe()

    const callsAtUnsubscribe = fetchMock.mock.calls.length
    await new Promise((r) => setTimeout(r, 1100))
    expect(fetchMock.mock.calls.length).toBe(callsAtUnsubscribe)
  })
})
