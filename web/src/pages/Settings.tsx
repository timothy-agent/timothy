import type { ComponentType } from 'react'
import { Navigate, Route, Routes, useParams } from 'react-router'
import { AgentsTab } from '../components/settings/AgentsTab'
import { ConnectorsTab } from '../components/settings/ConnectorsTab'
import { CredentialsTab } from '../components/settings/CredentialsTab'
import { DestinationsTab } from '../components/settings/DestinationsTab'
import { FeaturesTab } from '../components/settings/FeaturesTab'
import { ProvidersTab } from '../components/settings/ProvidersTab'
import { RoutesTab } from '../components/settings/RoutesTab'
import { SecretsTab } from '../components/settings/SecretsTab'
import { SettingsNav } from '../components/settings/SettingsNav'
import { settingsAreas, type SettingsAreaKey } from '../components/settings/settingsAreas'

export { settingsAreas }

// areaComponents maps each settings area key to the component it
// routes to; settingsAreas itself carries no render field so it stays
// usable from the sidebar and SettingsNav without pulling in every
// area's implementation.
const areaComponents: Record<SettingsAreaKey, ComponentType> = {
  providers: ProvidersTab,
  connectors: ConnectorsTab,
  agents: AgentsTab,
  routes: RoutesTab,
  secrets: SecretsTab,
  credentials: CredentialsTab,
  destinations: DestinationsTab,
  features: FeaturesTab,
}

// SettingsPage is the shared shell every settings area renders inside:
// SettingsNav down the side (or on top on mobile), the area's own
// component filling the rest. Each area renders its own PageHeader.
function SettingsPage({ area }: { area: (typeof settingsAreas)[number] }) {
  const Area = areaComponents[area.key]
  return (
    <div className="flex h-full min-h-0 flex-col md:flex-row">
      <SettingsNav areas={settingsAreas} current={area.key} />
      <div className="min-w-0 flex-1 overflow-y-auto">
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
