import { useSearchParams } from 'react-router'
import { AgentsTab } from '../components/settings/AgentsTab'
import { ConnectorsTab } from '../components/settings/ConnectorsTab'
import { FeaturesTab } from '../components/settings/FeaturesTab'
import { ProvidersTab } from '../components/settings/ProvidersTab'
import { RoutesTab } from '../components/settings/RoutesTab'
import { SecretsTab } from '../components/settings/SecretsTab'

// Tabs are URL-synced (?tab=…): a refresh or shared link lands on the
// same tab instead of resetting to Providers.
const tabs = [
  { key: 'agents', label: 'Agents', render: AgentsTab },
  { key: 'providers', label: 'Providers', render: ProvidersTab },
  { key: 'connectors', label: 'Connectors', render: ConnectorsTab },
  { key: 'routes', label: 'Routing', render: RoutesTab },
  { key: 'secrets', label: 'Secrets', render: SecretsTab },
  { key: 'features', label: 'Features', render: FeaturesTab },
] as const

export function Settings() {
  const [params, setParams] = useSearchParams()
  const active = tabs.find((t) => t.key === params.get('tab')) ?? tabs[0]

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
            <button
              key={t.key}
              type="button"
              onClick={() => setParams({ tab: t.key }, { replace: true })}
              className={
                active.key === t.key
                  ? 'border-b-2 border-blue-500 px-3 py-2 text-sm font-medium'
                  : 'px-3 py-2 text-sm text-muted-foreground hover:text-foreground'
              }
            >
              {t.label}
            </button>
          ))}
        </div>
        <active.render />
      </div>
    </div>
  )
}
