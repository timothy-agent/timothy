// One area per settings page, each a real route under /settings/*,
// not a query param, so a provider's own add/edit page (their own
// screen, not a dialog) has somewhere to live: /settings/providers/new.
// The sidebar's Settings submenu (App.tsx) and SettingsNav both read
// this same list, so they stay in lockstep.
export const settingsAreas = [
  {
    key: 'providers',
    label: 'Providers',
    description: 'Connect and manage the LLM providers Timothy can route work to.',
  },
  {
    key: 'connectors',
    label: 'Connectors',
    description: 'External services Timothy can act on, like Google, Outlook, or MCP servers.',
  },
  {
    key: 'agents',
    label: 'Agents',
    description: 'Prompt overlays, skills, and tools bundled per agent.',
  },
  {
    key: 'routes',
    label: 'Routing',
    description: 'Task routes decide which provider chain handles a given job.',
  },
  {
    key: 'secrets',
    label: 'Secrets',
    description: 'Where credentials live: Timothy storage, Vault, or AWS Secrets Manager.',
  },
  {
    key: 'credentials',
    label: 'Credentials',
    description: 'API keys and tokens stored for providers and connectors.',
  },
  {
    key: 'destinations',
    label: 'Destinations',
    description: 'Where mission results get delivered: email, webhook.',
  },
  {
    key: 'features',
    label: 'Features',
    description: 'Feature switches and defaults: changes serve immediately, no restarts.',
  },
] as const

export type SettingsAreaKey = (typeof settingsAreas)[number]['key']

export function settingsArea(key: SettingsAreaKey) {
  return settingsAreas.find((a) => a.key === key)!
}
