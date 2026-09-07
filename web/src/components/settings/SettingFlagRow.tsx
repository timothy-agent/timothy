import { Panel } from '../timothy/panel'
import { Switch } from '../ui/switch'

interface SettingFlagRowProps {
  id: string
  label: string
  description: string
  checked: boolean
  onChange: (v: boolean) => void
  busy?: boolean
}

// One feature flag as its own Panel: label, description, an immediate
// Switch (section 14.14). The container commits on change, so the
// Switch never sits behind a Save.
export function SettingFlagRow({ id, label, description, checked, onChange, busy }: SettingFlagRowProps) {
  return (
    <Panel headingLevel="h2">
      <div className="flex items-center gap-4">
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium">{label}</div>
          <p className="mt-0.5 text-sm text-muted-foreground">{description}</p>
        </div>
        <Switch id={id} checked={checked} onCheckedChange={onChange} disabled={busy} aria-label={label} />
      </div>
    </Panel>
  )
}
