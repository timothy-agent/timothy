import { afterEach, describe, expect, it, vi } from 'vitest'
import { uuid } from './uuid'

const v4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

afterEach(() => vi.unstubAllGlobals())

describe('uuid', () => {
  it('uses crypto.randomUUID when the context has it', () => {
    const randomUUID = vi.fn(() => '11111111-2222-4333-8444-555555555555')
    vi.stubGlobal('crypto', { ...crypto, randomUUID, getRandomValues: crypto.getRandomValues.bind(crypto) })
    expect(uuid()).toBe('11111111-2222-4333-8444-555555555555')
    expect(randomUUID).toHaveBeenCalledOnce()
  })

  it('falls back to a v4 id over plain http, where randomUUID is missing', () => {
    vi.stubGlobal('crypto', { getRandomValues: crypto.getRandomValues.bind(crypto) })
    const a = uuid()
    const b = uuid()
    expect(a).toMatch(v4)
    expect(b).toMatch(v4)
    expect(a).not.toBe(b)
  })
})
