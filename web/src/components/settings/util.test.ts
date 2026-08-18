import { describe, expect, it } from 'vitest'
import { humanizeProbeDetail, probeFailureText } from './util'

describe('humanizeProbeDetail', () => {
  it('extracts the message from an OpenAI-shaped JSON body', () => {
    expect(
      humanizeProbeDetail(
        'http 401: {"error":{"code":"401","message":"token expired or incorrect"}}',
      ),
    ).toBe('Provider rejected the API key — token expired or incorrect')
  })

  it('leaves a non-JSON probe detail unchanged', () => {
    expect(humanizeProbeDetail('upstream status 401')).toBe('upstream status 401')
  })

  it('uses the status label when the body is JSON without a message', () => {
    expect(humanizeProbeDetail('http 429: {"error":{"code":"429"}}')).toBe('Rate limited')
  })
})

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
