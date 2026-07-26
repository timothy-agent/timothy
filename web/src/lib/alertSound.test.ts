import { afterEach, describe, expect, it, vi } from 'vitest'
import { playAlertSound } from './alertSound'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('playAlertSound', () => {
  it('does nothing when AudioContext is unavailable (e.g. unsupported browser)', () => {
    vi.stubGlobal('AudioContext', undefined)
    expect(() => playAlertSound()).not.toThrow()
  })

  it('starts two oscillators through a fresh AudioContext', () => {
    const start = vi.fn()
    const stop = vi.fn()
    const connect = vi.fn()
    const oscillator = { type: '', frequency: { value: 0 }, connect, start, stop }
    const gain = {
      gain: {
        setValueAtTime: vi.fn(),
        exponentialRampToValueAtTime: vi.fn(),
      },
      connect: vi.fn(),
    }
    const close = vi.fn()
    const ctx = {
      currentTime: 0,
      createOscillator: vi.fn(() => oscillator),
      createGain: vi.fn(() => gain),
      destination: {},
      close,
    }
    const MockAudioContext = vi.fn(function AudioContext(this: unknown) {
      return ctx
    })
    vi.stubGlobal('AudioContext', MockAudioContext)

    playAlertSound()

    expect(MockAudioContext).toHaveBeenCalledTimes(1)
    expect(ctx.createOscillator).toHaveBeenCalledTimes(2)
    expect(start).toHaveBeenCalledTimes(2)
  })

  it('swallows errors instead of throwing (e.g. blocked autoplay)', () => {
    vi.stubGlobal(
      'AudioContext',
      vi.fn(function AudioContext() {
        throw new Error('blocked')
      }),
    )
    expect(() => playAlertSound()).not.toThrow()
  })
})
