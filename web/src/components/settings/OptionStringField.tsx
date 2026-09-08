import { Field } from '../timothy/field'
import { Input } from '../ui/input'

interface OptionStringFieldProps {
  label: string
  description?: string
  value: string
  onChange: (v: string) => void
  mono?: boolean
  placeholder?: string
}

// OptionStringField is a plain staged text field for a provider's
// options map entry (request timeout, litellm_provider, ...). Omitting
// the key when the value is empty is the page's buildPatch()
// responsibility, not this component's.
export function OptionStringField({ label, description, value, onChange, mono, placeholder }: OptionStringFieldProps) {
  return (
    <Field label={label} description={description} required={false}>
      {(props) => (
        <Input
          {...props}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder}
          className={mono ? 'font-mono' : undefined}
        />
      )}
    </Field>
  )
}
