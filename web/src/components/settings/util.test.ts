import { describe, expect, it } from 'vitest'
import { probeFailureText, responsesSuffix } from './util'

describe('probeFailureText', () => {
  it('does not render raw JSON in the connection-test banner', () => {
    expect(
      probeFailureText({
        latency_ms: 1694,
        detail: 'http 401: {"error":{"code":"401","message":"token expired or incorrect"}}',
      }),
    ).toBe('Failed after 1694 ms: Provider rejected the API key — token expired or incorrect')
  })
})

describe('responsesSuffix', () => {
  it('renders yes when the endpoint serves /responses', () => {
    expect(responsesSuffix({ responses_ok: true })).toBe(' · responses API: yes')
  })

  it('renders no on a confirmed miss (the Z.ai coding-plan case)', () => {
    expect(responsesSuffix({ responses_ok: false })).toBe(' · responses API: no')
  })

  it('renders nothing when the probe is unprobed or ambiguous', () => {
    expect(responsesSuffix({})).toBe('')
  })
})
