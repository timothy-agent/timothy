import { useEffect, useState } from 'react'
import { listSecretRefs, type SecretRefEntry } from '../../api/client'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Field } from './shared'

export type CredentialMode = 'new' | 'existing'

// referentLabel renders a ref's used-by hint for the option label — no
// type-classification of secrets, just what already references it.
function referentLabel(ref: SecretRefEntry): string {
  const refs = ref.referenced_by ?? []
  if (refs.length === 0) return ref.name
  return `${ref.name} (used by ${refs.map((r) => r.name).join(', ')})`
}

// managedRoleSuffix flags a ref as machine-managed, never a valid
// manual pick: a google connector's OAuth token bundle, or a github
// connector's derived signing key.
function managedRoleSuffix(ref: SecretRefEntry): string | null {
  const refs = ref.referenced_by ?? []
  if (refs.some((r) => r.role === 'oauth_tokens')) return ' — OAuth tokens (managed by connector)'
  if (refs.some((r) => r.role === 'signing_key')) return ' — signing key (managed)'
  return null
}

// ModeToggle is the segmented "New credential" / "Use existing"
// control shared by every form offering credential reuse.
export function CredentialModeToggle({
  mode,
  onChange,
  labels,
}: {
  mode: CredentialMode
  onChange: (mode: CredentialMode) => void
  // labels override the segment text where "New credential" would
  // mislead (e.g. rotating a token writes the current ref's value,
  // it never creates a credential).
  labels?: { new: string; existing: string }
}) {
  return (
    <div className="inline-flex rounded-lg border border-border p-0.5 text-sm">
      {(['new', 'existing'] as const).map((m) => (
        <button
          key={m}
          type="button"
          onClick={() => onChange(m)}
          className={`rounded-md px-2.5 py-1 font-medium transition ${
            mode === m ? 'bg-muted text-foreground' : 'text-muted-foreground hover:text-foreground'
          }`}
        >
          {m === 'new' ? (labels?.new ?? 'New credential') : (labels?.existing ?? 'Use existing')}
        </button>
      ))}
    </div>
  )
}

// ExistingCredentialSelect lists every stored ref (fetched fresh on
// mount) for a "Use existing" picker. Choosing one is the caller's
// responsibility to wire into credential_ref; this component only
// lists and reports the choice.
export function ExistingCredentialSelect({
  value,
  onChange,
  placeholder = 'choose a stored credential',
}: {
  value: string
  onChange: (refName: string) => void
  placeholder?: string
}) {
  const [refs, setRefs] = useState<SecretRefEntry[]>([])
  useEffect(() => {
    listSecretRefs().then(setRefs, () => undefined)
  }, [])

  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger className="mt-1.5 h-10 w-full" aria-label="existing credential">
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        {refs.map((r) => {
          const managedSuffix = managedRoleSuffix(r)
          return (
            <SelectItem key={r.name} value={r.name} disabled={managedSuffix !== null}>
              {referentLabel(r)}
              {managedSuffix}
            </SelectItem>
          )
        })}
      </SelectContent>
    </Select>
  )
}

// CredentialRefField pairs the mode toggle with either the caller's own
// "new credential" fields (children) or the existing-ref picker —
// choosing existing sets credential_ref to that name; the caller skips
// its own secret PUT on submit whenever mode is "existing".
export function CredentialRefField({
  label,
  mode,
  onModeChange,
  existingRef,
  onExistingRefChange,
  children,
}: {
  label: string
  mode: CredentialMode
  onModeChange: (mode: CredentialMode) => void
  existingRef: string
  onExistingRefChange: (refName: string) => void
  children: React.ReactNode
}) {
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium text-foreground">{label}</span>
        <CredentialModeToggle mode={mode} onChange={onModeChange} />
      </div>
      {mode === 'existing' ? (
        <Field label="Existing credential">
          <ExistingCredentialSelect value={existingRef} onChange={onExistingRefChange} />
        </Field>
      ) : (
        children
      )}
    </div>
  )
}
