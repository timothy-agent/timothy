// backendLabel names a secret's storage in UI copy.
const backendLabels: Record<string, string> = { db: 'encrypted', vault: 'vault', asm: 'aws' }
export function backendLabel(b: string): string {
  return backendLabels[b] ?? b
}

export function errText(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

// secretField shapes a credential input for the store-wide default
// backend: built-in storage takes the value itself (masked), an
// external backend takes the reference of a secret already there.
export function secretField(
  backend: string,
  dbPlaceholder: string,
): { type: 'password' | 'text'; placeholder: string; hint: string } {
  switch (backend) {
    case 'vault':
      return {
        type: 'text',
        placeholder: 'Vault path, e.g. timothy/anthropic#api_key',
        hint: 'Default backend is Vault — paste the path of the secret, not the secret itself.',
      }
    case 'asm':
      return {
        type: 'text',
        placeholder: 'ASM name or ARN, optional #json_key',
        hint: 'Default backend is AWS Secrets Manager — paste the secret name, not the secret itself.',
      }
    default:
      return { type: 'password', placeholder: dbPlaceholder, hint: '' }
  }
}
