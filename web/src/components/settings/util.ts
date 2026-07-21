// backendLabel names a secret's storage in UI copy.
const backendLabels: Record<string, string> = { db: 'encrypted', vault: 'vault', asm: 'aws' }
export function backendLabel(b: string): string {
  return backendLabels[b] ?? b
}

export function errText(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}
