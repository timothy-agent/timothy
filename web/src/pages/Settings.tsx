import { Navigate, Route, Routes, useLocation, Link } from 'react-router'
import { AgentsTab } from '../components/settings/AgentsTab'
import { ConnectorsTab } from '../components/settings/ConnectorsTab'
import { FeaturesTab } from '../components/settings/FeaturesTab'
import { ProvidersTab } from '../components/settings/ProvidersTab'
import { RoutesTab } from '../components/settings/RoutesTab'
import { SecretsTab } from '../components/settings/SecretsTab'
import { cn } from '../lib/utils'

// Sub-nav is a fixed tab list, one per settings area — each area is a
// real route under /settings/*, not a query param, so a provider's
// own add/edit page (their own screen, not a dialog) has somewhere to
// live: /settings/providers/new.
const tabs = [
  { key: 'providers', label: 'Providers', render: ProvidersTab },
  { key: 'connectors', label: 'Connectors', render: ConnectorsTab },
  { key: 'agents', label: 'Agents', render: AgentsTab },
  { key: 'routes', label: 'Routing', render: RoutesTab },
  { key: 'secrets', label: 'Secrets', render: SecretsTab },
  { key: 'features', label: 'Features', render: FeaturesTab },
] as const

export function Settings() {
  const { pathname } = useLocation()
  const active = tabs.find((t) => pathname.startsWith(`/settings/${t.key}`)) ?? tabs[0]

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto max-w-4xl py-8">
        <h1 className="text-2xl font-semibold tracking-tight">Settings</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Providers, task allocation, and feature switches — changes serve immediately, no
          restarts.
        </p>
        <div className="mt-5 flex gap-1 border-b border-border">
          {tabs.map((t) => (
            <Link
              key={t.key}
              to={`/settings/${t.key}`}
              className={cn(
                'px-3 py-2 text-sm font-medium',
                active.key === t.key
                  ? 'border-b-2 border-brand text-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              )}
            >
              {t.label}
            </Link>
          ))}
        </div>
        <Routes>
          <Route path="/" element={<Navigate to="providers" replace />} />
          {tabs.map((t) => (
            <Route key={t.key} path={`${t.key}/*`} element={<t.render />} />
          ))}
        </Routes>
      </div>
    </div>
  )
}
