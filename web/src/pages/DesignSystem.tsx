import { PageShell } from '@/components/timothy/page-shell'
import { PageHeader } from '@/components/timothy/page-header'
import { Foundations } from './design/Foundations'
import { Controls } from './design/Controls'
import { Compositions } from './design/Compositions'
import { AgentSurfaces } from './design/AgentSurfaces'

// Internal showcase of the Timothy design system contract. Not linked
// from the sidebar; every element here must follow the contract it
// demonstrates (tokens only, orange scarcity, comfortable vs
// operational density).
export function DesignSystem() {
  return (
    <PageShell width="full">
      <PageHeader
        title="Design system"
        description="A living reference of Timothy's tokens, components and density model."
        breadcrumbs={[{ label: 'Home', href: '/' }, { label: 'Design system' }]}
      />
      <div className="space-y-10">
        <Foundations />
        <Controls />
        <Compositions />
        <AgentSurfaces />
      </div>
    </PageShell>
  )
}
