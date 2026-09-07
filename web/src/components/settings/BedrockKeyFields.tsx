import { Field } from '../timothy/field'
import { Input } from '../ui/input'
import { stripPaste } from './util'

interface BedrockKeyFieldsProps {
  accessKeyId: string
  secretAccessKey: string
  onChange: (fields: { accessKeyId: string; secretAccessKey: string }) => void
  errors?: boolean
}

// bedrockKeyJSON builds the secret payload from the two key fields,
// shared by ProviderAdd and ProviderEdit so the JSON shape lives once.
export function bedrockKeyJSON(accessKeyId: string, secretAccessKey: string): string {
  return JSON.stringify({
    access_key_id: stripPaste(accessKeyId.trim()),
    secret_access_key: stripPaste(secretAccessKey.trim()),
  })
}

// BedrockKeyFields is the two-field AWS credential split every bedrock
// form uses, replacing a single generic key input.
export function BedrockKeyFields({ accessKeyId, secretAccessKey, onChange, errors }: BedrockKeyFieldsProps) {
  return (
    <div className="grid gap-5 sm:grid-cols-2">
      <Field label="Access Key ID">
        {(props) => (
          <Input
            {...props}
            type="password"
            value={accessKeyId}
            onChange={(e) => onChange({ accessKeyId: e.target.value, secretAccessKey })}
            placeholder="AKIA…"
            autoComplete="off"
            aria-invalid={errors || undefined}
          />
        )}
      </Field>
      <Field label="Secret Access Key">
        {(props) => (
          <Input
            {...props}
            type="password"
            value={secretAccessKey}
            onChange={(e) => onChange({ accessKeyId, secretAccessKey: e.target.value })}
            placeholder="wJalrXUtnFEMI/K7MDEN..."
            autoComplete="off"
            aria-invalid={errors || undefined}
          />
        )}
      </Field>
    </div>
  )
}
