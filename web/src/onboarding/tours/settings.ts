import type { TourDef } from '../tour/types'

// Anchors sit on the sidebar's Settings submenu; a closed submenu or an
// icon-collapsed sidebar hides them, so the tour waits for a later visit.
export const settingsTour: TourDef = {
  page: 'settings',
  version: 1,
  steps: [
    { target: 'settings.providers', title: 'Providers', body: 'Where models come from: OpenAI, Anthropic, Ollama and more.', side: 'right' },
    { target: 'settings.connectors', title: 'Connectors', body: 'Accounts Timothy can read and act through: mail, calendar, GitHub, cloud.', side: 'right' },
    { target: 'settings.agents', title: 'Agents', body: 'Named assistants with their own instructions, model, tools and knowledge.', side: 'right' },
    { target: 'settings.routes', title: 'Routing', body: 'Which model answers which kind of work, with fallbacks.', side: 'right' },
    { target: 'settings.secrets', title: 'Secret backends', body: 'Where keys are stored. The database backend works out of the box.', side: 'right' },
    { target: 'settings.credentials', title: 'Credentials', body: 'Every stored key by name. Values never show.', side: 'right' },
    { target: 'settings.destinations', title: 'Destinations', body: 'Where mission results go: pull requests, email, webhooks and channels.', side: 'right' },
    { target: 'settings.channels', title: 'Channels', body: 'Talk to Timothy from Telegram, Slack or email.', side: 'right' },
    { target: 'settings.features', title: 'Features', body: 'Switches and defaults for the whole instance.', side: 'right' },
  ],
}
