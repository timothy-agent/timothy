import { Eye, EyeOff } from 'lucide-react'
import { useEffect, useState } from 'react'
import { listSecretRefs, type SecretRefEntry } from '../../api/client'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { Input } from '../ui/input'
import { IconButton } from '../timothy/icon-button'
import { SegmentedControl } from '../timothy/segmented-control'
import { Field } from '../timothy/field'
import { secretDestination } from './util'

export type CredentialMode = 'new' | 'existing'

// referentLabel renders a ref's used-by hint for the option label, no
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
  if (refs.some((r) => r.role === 'oauth_tokens')) return ' - OAuth tokens (managed by connector)'
  if (refs.some((r) => r.role === 'signing_key')) return ' - signing key (managed)'
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
    <SegmentedControl
      value={mode}
      onChange={(v) => onChange(v as CredentialMode)}
      options={[
        { value: 'new', label: labels?.new ?? 'New credential' },
        { value: 'existing', label: labels?.existing ?? 'Use existing' },
      ]}
      size="sm"
      aria-label="Credential source"
    />
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

// CredentialField pairs the mode SegmentedControl with either a
// write-only new-secret password input (reveal toggle, secretDestination
// caption) or the existing-ref picker. Choosing existing sets
// credential_ref to that name and the caller skips its own secret write
// on submit.
export function CredentialField({
  label,
  mode,
  onModeChange,
  existingRef,
  onExistingRefChange,
  secretValue,
  onSecretValueChange,
  secretPlaceholder,
  defaultBackend,
  refName,
  modeLabels,
  invalid,
}: {
  label: string
  mode: CredentialMode
  onModeChange: (mode: CredentialMode) => void
  existingRef: string
  onExistingRefChange: (refName: string) => void
  secretValue: string
  onSecretValueChange: (v: string) => void
  secretPlaceholder?: string
  defaultBackend: string
  refName: string
  modeLabels?: { new: string; existing: string }
  invalid?: boolean
}) {
  const [revealed, setRevealed] = useState(false)

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium text-foreground">{label}</span>
        <CredentialModeToggle mode={mode} onChange={onModeChange} labels={modeLabels} />
      </div>
      {mode === 'existing' ? (
        <Field label="Existing credential">
          <ExistingCredentialSelect value={existingRef} onChange={onExistingRefChange} />
        </Field>
      ) : (
        <div className="space-y-1.5">
          <div className="flex gap-2">
            <Input
              type={revealed ? 'text' : 'password'}
              value={secretValue}
              onChange={(e) => onSecretValueChange(e.target.value)}
              placeholder={secretPlaceholder}
              aria-label={label}
              autoComplete="off"
              aria-invalid={invalid || undefined}
            />
            <IconButton
              label={revealed ? 'Hide token' : 'Show token'}
              icon={revealed ? EyeOff : Eye}
              variant="outline"
              tooltip={false}
              onClick={() => setRevealed((v) => !v)}
            />
          </div>
          <p className="text-sm text-muted-foreground">{secretDestination(defaultBackend, refName)}</p>
        </div>
      )}
    </div>
  )
}
