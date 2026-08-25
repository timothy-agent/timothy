import { Navigate, Route, Routes, useParams } from 'react-router'
import { AgentsTab } from '../components/settings/AgentsTab'
import { ConnectorsTab } from '../components/settings/ConnectorsTab'
import { CredentialsTab } from '../components/settings/CredentialsTab'
import { DestinationsTab } from '../components/settings/DestinationsTab'
import { FeaturesTab } from '../components/settings/FeaturesTab'
import { ProvidersTab } from '../components/settings/ProvidersTab'
import { RoutesTab } from '../components/settings/RoutesTab'
import { SecretsTab } from '../components/settings/SecretsTab'

// One area per settings page — each is a real route under
// /settings/*, not a query param, so a provider's own add/edit page
// (their own screen, not a dialog) has somewhere to live:
// /settings/providers/new. The sidebar's Settings submenu (App.tsx)
// links to these same keys, so they stay in lockstep.
export const settingsAreas = [
  {
    key: 'providers',
    label: 'Providers',
    description: 'Connect and manage the LLM providers Timothy can route work to.',
    render: ProvidersTab,
  },
  {
    key: 'connectors',
    label: 'Connectors',
    description: 'External services Timothy can act on, like Google, Outlook, or MCP servers.',
    render: ConnectorsTab,
  },
  {
    key: 'agents',
    label: 'Agents',
    description: 'Prompt overlays, skills, and tools bundled per agent.',
    render: AgentsTab,
  },
  {
    key: 'routes',
    label: 'Routing',
    description: 'Task routes decide which provider chain handles a given job.',
    render: RoutesTab,
  },
  {
    key: 'secrets',
    label: 'Secrets',
    description: 'Where credentials live: Timothy storage, Vault, or AWS Secrets Manager.',
    render: SecretsTab,
  },
  {
    key: 'credentials',
    label: 'Credentials',
    description: 'API keys and tokens stored for providers and connectors.',
    render: CredentialsTab,
  },
  {
    key: 'destinations',
    label: 'Destinations',
    description: 'Where mission results get delivered: email, webhook.',
    render: DestinationsTab,
  },
  {
    key: 'features',
    label: 'Features',
    description: 'Feature switches and defaults: changes serve immediately, no restarts.',
    render: FeaturesTab,
  },
] as const

// SettingsPage is the shared shell every settings area renders inside:
// same container/heading style as other top-level pages (Memory,
// Analytics), just the area's own component below it.
function SettingsPage({ area }: { area: (typeof settingsAreas)[number] }) {
  const Area = area.render
  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto max-w-full px-8 py-8">
        <h1 className="text-2xl font-semibold tracking-tight">{area.label}</h1>
        <p className="mt-1 text-sm text-muted-foreground">{area.description}</p>
        <Area />
      </div>
    </div>
  )
}

// KnowledgeRedirect keeps old /settings/knowledge(/...) links working
// after the area moved to its own top-level page.
function KnowledgeRedirect() {
  const { '*': rest } = useParams()
  return <Navigate to={`/knowledge${rest ? `/${rest}` : ''}`} replace />
}

export function Settings() {
  return (
    <Routes>
      <Route path="/" element={<Navigate to="providers" replace />} />
      <Route path="knowledge/*" element={<KnowledgeRedirect />} />
      {settingsAreas.map((area) => (
        <Route key={area.key} path={`${area.key}/*`} element={<SettingsPage area={area} />} />
      ))}
    </Routes>
  )
}
