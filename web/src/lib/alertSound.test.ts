import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// sharedCtx (module state) persists across tests within this file, so
// each test gets its own fresh mock AudioContext constructor and
// resets vi's module registry to force alertSound.ts to re-evaluate
// with a clean sharedCtx — otherwise test 2 would reuse test 1's mock
// context instead of constructing its own.
beforeEach(() => {
  vi.resetModules()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

function mockAudioContext() {
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
  const resume = vi.fn()
  const ctx = {
    currentTime: 0,
    createOscillator: vi.fn(() => oscillator),
    createGain: vi.fn(() => gain),
    destination: {},
    resume,
  }
  const MockAudioContext = vi.fn(function AudioContext(this: unknown) {
    return ctx
  })
  return { MockAudioContext, ctx, resume, start }
}

describe('playAlertSound', () => {
  it('does nothing when AudioContext is unavailable (e.g. unsupported browser)', async () => {
    vi.stubGlobal('AudioContext', undefined)
    const { playAlertSound: play } = await import('./alertSound')
    expect(() => play()).not.toThrow()
  })

  it('starts two oscillators through the shared AudioContext', async () => {
    const { MockAudioContext, ctx, start } = mockAudioContext()
    vi.stubGlobal('AudioContext', MockAudioContext)
    const { playAlertSound: play } = await import('./alertSound')

    play()

    expect(MockAudioContext).toHaveBeenCalledTimes(1)
    expect(ctx.createOscillator).toHaveBeenCalledTimes(2)
    expect(start).toHaveBeenCalledTimes(2)
  })

  it('reuses the same AudioContext across repeated calls', async () => {
    const { MockAudioContext } = mockAudioContext()
    vi.stubGlobal('AudioContext', MockAudioContext)
    const { playAlertSound: play } = await import('./alertSound')

    play()
    play()

    expect(MockAudioContext).toHaveBeenCalledTimes(1)
  })

  it('swallows errors instead of throwing (e.g. blocked autoplay)', async () => {
    vi.stubGlobal(
      'AudioContext',
      vi.fn(function AudioContext() {
        throw new Error('blocked')
      }),
    )
    const { playAlertSound: play } = await import('./alertSound')
    expect(() => play()).not.toThrow()
  })
})

describe('unlockAudio', () => {
  it('resumes the shared AudioContext', async () => {
    const { MockAudioContext, resume } = mockAudioContext()
    vi.stubGlobal('AudioContext', MockAudioContext)
    const { unlockAudio: unlock } = await import('./alertSound')

    unlock()

    expect(resume).toHaveBeenCalledTimes(1)
  })

  it('does nothing when AudioContext is unavailable', async () => {
    vi.stubGlobal('AudioContext', undefined)
    const { unlockAudio: unlock } = await import('./alertSound')
    expect(() => unlock()).not.toThrow()
  })

  it('swallows a resume() failure instead of throwing', async () => {
    const { MockAudioContext, resume } = mockAudioContext()
    resume.mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.stubGlobal('AudioContext', MockAudioContext)
    const { unlockAudio: unlock } = await import('./alertSound')
    expect(() => unlock()).not.toThrow()
  })
})
