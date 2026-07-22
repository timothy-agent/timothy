// ConnectorPreset is one entry of the declarative connector registry:
// the tile grid and connect dialog render from these. kind maps to the
// backend's builder ('mcp' | 'google').
export interface ConnectorPreset {
  id: string
  name: string
  kind: 'mcp' | 'google'
  description: string
  logo?: string
  brandColor: string
  // mcp: default endpoint ('' = user must enter one)
  endpoint?: string
  endpointHint?: string
  tokenPlaceholder?: string
  tokenHint?: string
  // google: OAuth scopes this preset requests
  scopes?: string[]
}

export const gmailScope = 'https://www.googleapis.com/auth/gmail.modify'
export const calendarScope = 'https://www.googleapis.com/auth/calendar'

export const connectorPresets: ConnectorPreset[] = [
  {
    id: 'gmail',
    name: 'Gmail',
    kind: 'google',
    description: 'Read, search, and send email',
    logo: 'gmail',
    brandColor: '#EA4335',
    scopes: [gmailScope],
  },
  {
    id: 'google-calendar',
    name: 'Google Calendar',
    kind: 'google',
    description: 'List and create events',
    logo: 'googlecalendar',
    brandColor: '#4285F4',
    scopes: [calendarScope],
  },
  {
    id: 'github',
    name: 'GitHub',
    kind: 'mcp',
    description: 'Issues, PRs, code — via MCP',
    logo: 'github',
    brandColor: '#24292F',
    endpoint: 'https://api.githubcopilot.com/mcp/',
    tokenPlaceholder: 'ghp_… or github_pat_…',
    tokenHint: 'A personal access token; fine-grained tokens work. github.com/settings/tokens',
  },
]

// Fallback for connectors that predate a preset removal / don't match
// any current preset — keeps ConnectorLogo/name rendering safe without
// needing a dummy entry in the list above.
export const unknownPreset: ConnectorPreset = {
  id: 'unknown',
  name: 'Custom',
  kind: 'mcp',
  description: '',
  brandColor: '#4B5563',
}
