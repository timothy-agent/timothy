import { Textarea } from '../ui/textarea'

// GCPKeyField is the service-account key paste input. A key is
// multi-line JSON, so this is a textarea rather than the single-line
// password input every other credential uses, and the value is stored
// verbatim: the Go side parses it as JSON.
export function GCPKeyField({
  value,
  onChange,
}: {
  value: string
  onChange: (v: string) => void
}) {
  return (
    <Textarea
      value={value}
      onChange={(e) => onChange(e.target.value)}
      placeholder='{"type": "service_account", "project_id": "…", "private_key": "…"}'
      aria-label="Service account key"
      rows={6}
      autoComplete="off"
      spellCheck={false}
      className="font-mono text-xs"
    />
  )
}
