// backendLabel names a secret's storage in UI copy.
const backendLabels: Record<string, string> = {
  db: 'encrypted',
  vault: 'vault',
  asm: 'aws',
}
export function backendLabel(b: string): string {
  return backendLabels[b] ?? b
}

export const timothyAuthErrorMessage =
  "Timothy's API token is missing or invalid. Paste TIMOTHY_API_TOKEN from deploy/.env — this is not an LLM provider key."

// Brain's own auth failures: status 401, code unauthorized /
// auth_not_configured, or the exact message the API returns. Match
// the message too so a stripped Error (no status) cannot paint as a
// provider probe miss.
const brainAuthMessage = /missing or invalid bearer token|TIMOTHY_API_TOKEN is not set/i

export function isTimothyAuthDetail(detail: string | undefined): boolean {
  return !!detail && brainAuthMessage.test(detail)
}

// Duck-typed so tests that mock api/client still work — same predicate
// as client.isTimothyAuthError (401 or auth_not_configured).
export function isTimothyAuthError(err: unknown): boolean {
  if (typeof err !== 'object' || err === null) return false
  const e = err as { status?: unknown; code?: unknown; message?: unknown }
  if (e.status === 401 || e.code === 'auth_not_configured' || e.code === 'unauthorized') return true
  return typeof e.message === 'string' && isTimothyAuthDetail(e.message)
}

export function errText(err: unknown): string {
  if (isTimothyAuthError(err)) return timothyAuthErrorMessage
  const raw = err instanceof Error ? err.message : String(err)
  return humanizeProbeDetail(raw)
}

export function probeFailureText(test: { latency_ms: number; detail?: string }): string {
  if (isTimothyAuthDetail(test.detail)) return timothyAuthErrorMessage
  return `Failed after ${test.latency_ms} ms: ${humanizeProbeDetail(test.detail ?? '')}`
}

// responsesSuffix appends the responses-API capability probe result to
// a passing test's success line — absent (unprobed or ambiguous) adds
// nothing.
export function responsesSuffix(test: { responses_ok?: boolean }): string {
  if (test.responses_ok === undefined) return ''
  return ` · responses API: ${test.responses_ok ? 'yes' : 'no'}`
}

function pickJsonMessage(v: unknown): string | undefined {
  if (typeof v === 'string' && v.trim()) return v.trim()
  if (typeof v !== 'object' || v === null) return undefined
  const o = v as Record<string, unknown>
  if (typeof o.message === 'string' && o.message.trim()) return o.message.trim()
  if (typeof o.error === 'string' && o.error.trim()) return o.error.trim()
  if (o.error !== undefined) {
    const inner = pickJsonMessage(o.error)
    if (inner) return inner
  }
  if (typeof o.msg === 'string' && o.msg.trim()) return o.msg.trim()
  if (typeof o.detail === 'string' && o.detail.trim()) return o.detail.trim()
  return undefined
}

function httpStatusLabel(status: number): string | undefined {
  switch (status) {
    case 401:
    case 403:
      return 'Provider rejected the API key'
    case 404:
      return 'Model or endpoint not found'
    case 429:
      return 'Rate limited'
    case 400:
      return 'Bad request'
    default:
      if (status >= 500) return 'Provider error'
      return undefined
  }
}

// humanizeProbeDetail turns gateway wire text like
// `http 401: {"error":{"message":"token expired or incorrect"}}`
// into a sentence the connection-test banner can show.
export function humanizeProbeDetail(detail: string): string {
  const m = detail.match(/^http (\d+):\s*([\s\S]*)$/i)
  if (!m) {
    const bare = detail.trim()
    if (bare.startsWith('{')) {
      try {
        return pickJsonMessage(JSON.parse(bare)) ?? detail
      } catch {
        return detail
      }
    }
    return detail
  }
  const status = Number(m[1])
  const rest = m[2].trim()
  let msg: string | undefined
  if (rest.startsWith('{') || rest.startsWith('[')) {
    try {
      msg = pickJsonMessage(JSON.parse(rest))
    } catch {
      msg = undefined
    }
  } else if (rest) {
    msg = rest
  }
  const label = httpStatusLabel(status)
  if (label && msg) return `${label} — ${msg}`
  if (msg) return msg
  if (label) return label
  return detail
}

// connectedAs labels a test-connection identity, skipping the email
// when it repeats the login (google/microsoft report the same address
// for both).
export function connectedAs(identity: { login: string; email: string }): string {
  if (identity.email && identity.email !== identity.login) {
    return `Connected as ${identity.login} (${identity.email})`
  }
  return `Connected as ${identity.login}`
}

// stripPaste removes whitespace and zero-width characters that ride
// along when a key is copied out of wrapped text.
export function stripPaste(v: string): string {
  return v.replace(/[\s​-‍⁠﻿]/g, '')
}

// secretDestination describes where a pasted credential ends up under
// the store-wide default backend — every backend now writes through
// Timothy, so this is copy only, never a field shape.
export function secretDestination(backend: string, ref: string): string {
  const name = ref.trim() || 'reference name'
  switch (backend) {
    case 'vault':
      return `Timothy stores the key in Vault (path timothy/${name}).`
    case 'asm':
      return `Timothy stores the key in AWS Secrets Manager (name timothy/${name}).`
    default:
      return "Encrypted with the master key and kept in Timothy's database."
  }
}
