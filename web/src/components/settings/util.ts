// backendLabel names a secret's storage in UI copy.
const backendLabels: Record<string, string> = {
  db: 'encrypted',
  vault: 'vault',
  asm: 'aws',
}
export function backendLabel(b: string): string {
  return backendLabels[b] ?? b
}

export function errText(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
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
