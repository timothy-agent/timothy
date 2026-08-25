// ConnectorPreset is one entry of the declarative connector registry:
// the tile grid and connect dialog render from these. kind maps to the
// backend's builder ('mcp' | 'google').
export interface ConnectorPreset {
  id: string
  name: string
  kind: 'mcp' | 'google' | 'github' | 'microsoft'
  description: string
  logo?: string
  brandColor: string
  // mcp: default endpoint ('' = user must enter one)
  endpoint?: string
  endpointHint?: string
  // mcp, github: PAT/bearer token input copy
  tokenPlaceholder?: string
  tokenHint?: string
  // github: link rendered after tokenHint (e.g. "Create one on GitHub")
  tokenURL?: string
  // google, microsoft: OAuth scopes this preset requests
  scopes?: string[]
}

export const gmailScope = 'https://www.googleapis.com/auth/gmail.modify'
export const calendarScope = 'https://www.googleapis.com/auth/calendar'
export const driveScope = 'https://www.googleapis.com/auth/drive.readonly'
export const docsScopes = ['https://www.googleapis.com/auth/documents', 'https://www.googleapis.com/auth/drive.file']
export const outlookScopes = ['Mail.Read', 'Mail.Send', 'Calendars.Read', 'offline_access', 'User.Read']

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
    id: 'google-drive',
    name: 'Google Drive',
    kind: 'google',
    description: 'Search and read files (read-only)',
    logo: 'googledrive',
    brandColor: '#0F9D58',
    scopes: [driveScope],
  },
  {
    id: 'google-docs',
    name: 'Google Docs',
    kind: 'google',
    description: 'Read, create, and append to docs',
    logo: 'googledocs',
    brandColor: '#4285F4',
    scopes: docsScopes,
  },
  {
    id: 'outlook',
    name: 'Outlook',
    kind: 'microsoft',
    description: 'Read, search, and send mail; list calendar events',
    logo: 'outlook',
    brandColor: '#0078D4',
    scopes: outlookScopes,
  },
  {
    id: 'github',
    name: 'GitHub MCP',
    kind: 'mcp',
    description: 'Issues, PRs, code, via MCP',
    logo: 'github',
    brandColor: '#24292F',
    endpoint: 'https://api.githubcopilot.com/mcp/',
    tokenPlaceholder: 'ghp_… or github_pat_…',
    tokenHint: 'A personal access token; fine-grained tokens work. github.com/settings/tokens',
  },
  {
    id: 'github-account',
    name: 'GitHub',
    kind: 'github',
    description: 'Identity for mission clone/push/PR, no chat tools',
    logo: 'github',
    brandColor: '#24292F',
    tokenPlaceholder: 'ghp_… or github_pat_…',
    tokenHint:
      'Fine-grained personal access token — grant Contents (read and write) and Pull requests on the repositories Timothy may work with.',
    tokenURL: 'https://github.com/settings/personal-access-tokens/new',
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
