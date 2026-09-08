import { describe, expect, it } from 'vitest'
import {
  errText,
  humanizeProbeDetail,
  isTimothyAuthDetail,
  isTimothyAuthError,
  timothyAuthErrorMessage,
} from './errors'

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

  it('labels a 404 as model or endpoint not found', () => {
    expect(humanizeProbeDetail('http 404: {"message":"no such model"}')).toBe(
      'Model or endpoint not found — no such model',
    )
  })

  it('labels a 400 as a bad request', () => {
    expect(humanizeProbeDetail('http 400: {"error":{"message":"missing field"}}')).toBe(
      'Bad request — missing field',
    )
  })

  it('labels a 5xx status as a provider error', () => {
    expect(humanizeProbeDetail('http 503: {"error":{"message":"down"}}')).toBe(
      'Provider error — down',
    )
  })

  it('falls back to the raw detail for an unlabeled status with no message', () => {
    expect(humanizeProbeDetail('http 418: {}')).toBe('http 418: {}')
  })

  it('uses the plain-text rest of an http-prefixed message when not JSON', () => {
    expect(humanizeProbeDetail('http 429: rate limit exceeded')).toBe(
      'Rate limited — rate limit exceeded',
    )
  })

  it('falls back to the status label when the JSON body fails to parse', () => {
    expect(humanizeProbeDetail('http 400: {not valid json')).toBe('Bad request')
  })

  it('falls back to the raw detail when JSON fails to parse and there is no status label', () => {
    expect(humanizeProbeDetail('http 418: {not valid json')).toBe('http 418: {not valid json')
  })

  it('parses a bare JSON object with no http prefix', () => {
    expect(humanizeProbeDetail('{"message":"bad token"}')).toBe('bad token')
  })

  it('falls back to the raw detail when a bare JSON-looking string fails to parse', () => {
    expect(humanizeProbeDetail('{not valid json')).toBe('{not valid json')
  })

  it('falls back to the raw detail when bare JSON parses but has no message field', () => {
    expect(humanizeProbeDetail('{"foo":"bar"}')).toBe('{"foo":"bar"}')
  })

  it('leaves plain non-JSON, non-http text unchanged', () => {
    expect(humanizeProbeDetail('plain text detail')).toBe('plain text detail')
  })

  it('extracts a nested error.error.message shape', () => {
    expect(
      humanizeProbeDetail('http 401: {"error":{"error":"token expired"}}'),
    ).toBe('Provider rejected the API key — token expired')
  })

  it('extracts a msg field when message and error are absent', () => {
    expect(humanizeProbeDetail('http 429: {"msg":"slow down"}')).toBe(
      'Rate limited — slow down',
    )
  })

  it('extracts a detail field when message, error and msg are absent', () => {
    expect(humanizeProbeDetail('http 429: {"detail":"too many requests"}')).toBe(
      'Rate limited — too many requests',
    )
  })

  it('parses a JSON array body', () => {
    expect(humanizeProbeDetail('http 429: [1,2,3]')).toBe('Rate limited')
  })

  it('uses the plain-text message alone when the status has no label', () => {
    expect(humanizeProbeDetail('http 418: teapot refused')).toBe('teapot refused')
  })

  it('falls back to the label alone when there is no message body', () => {
    expect(humanizeProbeDetail('http 400: ')).toBe('Bad request')
  })
})

describe('isTimothyAuthDetail', () => {
  it('returns false for undefined detail', () => {
    expect(isTimothyAuthDetail(undefined)).toBe(false)
  })

  it('matches the missing bearer token message', () => {
    expect(isTimothyAuthDetail('missing or invalid bearer token')).toBe(true)
  })

  it('matches the TIMOTHY_API_TOKEN not set message', () => {
    expect(isTimothyAuthDetail('TIMOTHY_API_TOKEN is not set')).toBe(true)
  })

  it('does not match an unrelated detail', () => {
    expect(isTimothyAuthDetail('rate limited')).toBe(false)
  })
})

describe('isTimothyAuthError', () => {
  it('returns false for non-object errors', () => {
    expect(isTimothyAuthError('boom')).toBe(false)
    expect(isTimothyAuthError(null)).toBe(false)
  })

  it('returns true for status 401', () => {
    expect(isTimothyAuthError({ status: 401 })).toBe(true)
  })

  it('returns true for code auth_not_configured', () => {
    expect(isTimothyAuthError({ code: 'auth_not_configured' })).toBe(true)
  })

  it('returns true for code unauthorized', () => {
    expect(isTimothyAuthError({ code: 'unauthorized' })).toBe(true)
  })

  it('returns true when the message matches the brain auth pattern', () => {
    expect(isTimothyAuthError({ message: 'TIMOTHY_API_TOKEN is not set' })).toBe(true)
  })

  it('returns false when message is not a string', () => {
    expect(isTimothyAuthError({ message: 42 })).toBe(false)
  })

  it('returns false for an unrelated error object', () => {
    expect(isTimothyAuthError({ status: 500, message: 'boom' })).toBe(false)
  })
})

describe('errText', () => {
  it('returns the Timothy auth error message for an auth error', () => {
    expect(errText({ status: 401 })).toBe(timothyAuthErrorMessage)
  })

  it('humanizes an Error instance message', () => {
    expect(errText(new Error('http 429: {"message":"slow down"}'))).toBe(
      'Rate limited — slow down',
    )
  })

  it('stringifies a non-Error value', () => {
    expect(errText('plain string error')).toBe('plain string error')
  })
})
