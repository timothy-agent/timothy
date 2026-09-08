import { humanizeProbeDetail, isTimothyAuthDetail, timothyAuthErrorMessage } from '@/lib/errors'

// sentinel Select value for "none / inherit / off" options, since
// Radix Select rejects an empty string as an item value.
export const UNSET = '__unset__'

// backendLabel names a secret's storage in UI copy.
const backendLabels: Record<string, string> = {
  db: 'encrypted',
  vault: 'vault',
  asm: 'aws',
}
export function backendLabel(b: string): string {
  return backendLabels[b] ?? b
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
