import { Link, useNavigate } from 'react-router'

import { useIsMobile } from '@/hooks/use-mobile'
import { cn } from '@/lib/utils'
import { Label } from '../ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import type { settingsAreas, SettingsAreaKey } from './settingsAreas'

// SettingsNav is the settings section's own navigation: a link list on
// desktop, an area picker on mobile. No heading of its own, the
// page's PageHeader carries that.
export function SettingsNav({
  areas,
  current,
}: {
  areas: typeof settingsAreas
  current: SettingsAreaKey
}) {
  const isMobile = useIsMobile()
  const navigate = useNavigate()

  if (isMobile) {
    return (
      <div className="border-b border-border px-4 py-3">
        <Label htmlFor="settings-area">Settings area</Label>
        <Select value={current} onValueChange={(key) => navigate(`/settings/${key}`)}>
          <SelectTrigger id="settings-area" className="mt-1.5 w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {areas.map((area) => (
              <SelectItem key={area.key} value={area.key}>
                {area.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    )
  }

  return (
    <nav aria-label="Settings" className="w-48 shrink-0 overflow-y-auto border-r border-border px-3 py-6">
      <ul>
        {areas.map((area) => {
          const isCurrent = area.key === current
          return (
            <li key={area.key}>
              <Link
                to={`/settings/${area.key}`}
                aria-current={isCurrent ? 'page' : undefined}
                className={cn(
                  'flex h-8 items-center px-2 text-sm',
                  isCurrent
                    ? 'bg-brand-soft font-medium text-brand-soft-foreground'
                    : 'text-muted-foreground hover:bg-accent hover:text-foreground',
                )}
              >
                {area.label}
              </Link>
            </li>
          )
        })}
      </ul>
    </nav>
  )
}
