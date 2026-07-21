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
  {
    id: 'grafana',
    name: 'Grafana',
    kind: 'mcp',
    description: 'Metrics, logs, alerts — via MCP',
    logo: 'grafana',
    brandColor: '#F46800',
    endpoint: '',
    endpointHint: 'Your Grafana MCP server endpoint, e.g. https://grafana.internal/mcp',
    tokenPlaceholder: 'service account token',
    tokenHint: 'A Grafana service-account token with the roles the tools need.',
  },
  {
    id: 'custom-mcp',
    name: 'Custom MCP server',
    kind: 'mcp',
    description: 'Any streamable-HTTP MCP endpoint',
    brandColor: '#4B5563',
    endpoint: '',
    endpointHint: 'The server’s streamable-HTTP endpoint',
    tokenHint: 'Optional — leave empty for servers without auth.',
  },
]
