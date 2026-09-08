import type { ReactNode } from 'react'
import { Panel } from '../timothy/panel'
import { Button } from '../ui/button'
import { Alert, AlertDescription } from '../ui/alert'

interface SettingValueCardProps {
  title: string
  description?: string
  dirty: boolean
  saving?: boolean
  error?: string | null
  onRetry?: () => void
  onSave?: () => void
  children: ReactNode
}

// One setting value as its own Panel: a field, explicit Save enabled
// only while dirty (section 14.14). A failed Save keeps the staged
// value and shows a persistent Alert with Retry (10.7). Omitting
// onSave (timezone: an atomic preference) renders the field alone,
// with no Save button.
export function SettingValueCard({
  title,
  description,
  dirty,
  saving,
  error,
  onRetry,
  onSave,
  children,
}: SettingValueCardProps) {
  return (
    <Panel title={title} headingLevel="h2">
      <div role="region" aria-label={title}>
        <div className="flex flex-wrap items-end gap-3">
          {children}
          {onSave && (
            <Button onClick={onSave} disabled={!dirty || saving}>
              Save
            </Button>
          )}
        </div>
        {description && <p className="mt-2 text-sm text-muted-foreground">{description}</p>}
        {error && (
          <Alert tone="destructive" className="mt-3">
            <AlertDescription>
              {error}
              <Button variant="outline" size="sm" className="ml-3" onClick={onRetry}>
                Retry
              </Button>
            </AlertDescription>
          </Alert>
        )}
      </div>
    </Panel>
  )
}
