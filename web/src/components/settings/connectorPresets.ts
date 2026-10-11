// ConnectorPreset is one entry of the declarative connector registry:
// the tile grid and connect dialog render from these. kind maps to the
// backend's builder ('mcp' | 'google').
export interface ConnectorPreset {
  id: string
  name: string
  kind: 'mcp' | 'google' | 'github' | 'microsoft' | 'imap' | 'caldav' | 'aws' | 'gcp' | 'bitbucket' | 'gitlab'
  description: string
  logo?: string
  brandColor: string
  // mcp: default endpoint ('' = user must enter one)
  endpoint?: string
  endpointHint?: string
  // mcp, github: PAT/bearer token input copy
  tokenPlaceholder?: string
  tokenHint?: string
  // github, bitbucket, gitlab: link rendered after tokenHint (e.g. "Create one on GitHub")
  tokenURL?: string
  // google, microsoft: OAuth scopes this preset requests
  scopes?: string[]
  // mcp: the authentication the add form starts on (default 'token')
  authMode?: 'token' | 'oauth'
  // mcp catalog (issue #1162): the provider's setup page, a note shown
  // in oauth mode (client registration quirks), and the day the
  // endpoint was last checked against that page.
  docsURL?: string
  authHint?: string
  verifiedOn?: string
}

const gmailScope = 'https://www.googleapis.com/auth/gmail.modify'
const calendarScope = 'https://www.googleapis.com/auth/calendar'
const driveScope = 'https://www.googleapis.com/auth/drive.readonly'
const docsScopes = ['https://www.googleapis.com/auth/documents', 'https://www.googleapis.com/auth/drive.file']
const outlookScopes = ['Mail.Read', 'Mail.Send', 'Calendars.Read', 'offline_access', 'User.Read']

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
    id: 'aws',
    name: 'AWS',
    kind: 'aws',
    description: 'Your AWS accounts via the AWS MCP Server: resources, docs, API queries',
    logo: 'aws',
    brandColor: '#FF9900',
    endpoint: 'https://aws-mcp.eu-central-1.api.aws/mcp',
  },
  {
    id: 'gcp',
    name: 'GCP',
    kind: 'gcp',
    description: 'A GCP project via a service-account key: Cloud Storage objects, BigQuery queries',
    logo: 'gcp',
    brandColor: '#4285F4',
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
    description: 'Identity for mission clone/push/PR, read-only pull request tools',
    logo: 'github',
    brandColor: '#24292F',
    tokenPlaceholder: 'ghp_… or github_pat_…',
    tokenHint:
      'Fine-grained personal access token. Grant Contents (read and write) and Pull requests on the repositories Timothy may work with.',
    tokenURL: 'https://github.com/settings/personal-access-tokens/new',
  },
  {
    id: 'bitbucket-account',
    name: 'Bitbucket',
    kind: 'bitbucket',
    description: 'Identity for mission clone/push/PR, read-only pull request tools',
    logo: 'bitbucket',
    brandColor: '#0052CC',
    tokenPlaceholder: 'workspace or repository access token',
    tokenHint:
      'Bitbucket Cloud workspace or repository access token. Grant Repositories: Read and Pull requests: Read on the repositories Timothy may work with.',
    tokenURL: 'https://support.atlassian.com/bitbucket-cloud/docs/access-tokens/',
  },
  {
    id: 'gitlab-account',
    name: 'GitLab',
    kind: 'gitlab',
    description: 'Identity for mission clone/push/PR, read-only pull request tools',
    logo: 'gitlab',
    brandColor: '#FC6D26',
    tokenPlaceholder: 'glpat-…',
    tokenHint:
      'A personal or project access token with the api and write_repository scopes on the projects Timothy may work with.',
    tokenURL: 'https://gitlab.com/-/user_settings/personal_access_tokens',
  },
  {
    id: 'imap',
    name: 'IMAP mailbox',
    kind: 'imap',
    description: 'Any email account via IMAP, optional SMTP sending.',
    brandColor: '#64748B',
  },
  {
    id: 'caldav',
    name: 'CalDAV calendar',
    kind: 'caldav',
    description: 'Any calendar via CalDAV, list and create events.',
    brandColor: '#64748B',
  },
  {
    id: 'notion',
    name: 'Notion',
    kind: 'mcp',
    description: 'Pages, databases and comments via Notion’s hosted MCP server',
    logo: 'notion',
    brandColor: '#000000',
    endpoint: 'https://mcp.notion.com/mcp',
    authMode: 'oauth',
    docsURL: 'https://developers.notion.com/docs/get-started-with-mcp',
    verifiedOn: '2026-10-11',
  },
  {
    id: 'slack',
    name: 'Slack',
    kind: 'mcp',
    description: 'Search and read messages, post to channels via Slack’s hosted MCP server',
    logo: 'slack',
    brandColor: '#4A154B',
    endpoint: 'https://mcp.slack.com/mcp',
    authMode: 'oauth',
    authHint:
      'Slack has no automatic client registration: create a Slack app, allow MCP on it, and paste its client ID and secret.',
    docsURL: 'https://docs.slack.dev/ai/slack-mcp-server/',
    verifiedOn: '2026-10-11',
  },
  {
    id: 'linear',
    name: 'Linear',
    kind: 'mcp',
    description: 'Issues, projects and cycles via Linear’s hosted MCP server',
    logo: 'linear',
    brandColor: '#5E6AD2',
    endpoint: 'https://mcp.linear.app/mcp',
    authMode: 'oauth',
    tokenHint: 'A Linear API key also works as the bearer token; it acts as one shared identity.',
    docsURL: 'https://linear.app/docs/mcp',
    verifiedOn: '2026-10-11',
  },
  {
    id: 'atlassian',
    name: 'Atlassian',
    kind: 'mcp',
    description: 'Jira issues and Confluence pages via the Atlassian Rovo MCP server',
    logo: 'atlassian',
    brandColor: '#0052CC',
    endpoint: 'https://mcp.atlassian.com/v1/mcp/authv2',
    authMode: 'oauth',
    docsURL: 'https://support.atlassian.com/atlassian-ai-gateway/docs/get-started-with-the-atlassian-remote-mcp-server/',
    verifiedOn: '2026-10-11',
  },
  {
    id: 'hubspot',
    name: 'HubSpot',
    kind: 'mcp',
    description: 'CRM records and activities via HubSpot’s hosted MCP server',
    logo: 'hubspot',
    brandColor: '#FF7A59',
    endpoint: 'https://mcp.hubspot.com/',
    authMode: 'oauth',
    authHint:
      'Create an MCP connector under Development in your HubSpot account and paste its client ID and secret.',
    docsURL: 'https://developers.hubspot.com/docs/apps/developer-platform/build-apps/integrate-with-the-remote-hubspot-mcp-server',
    verifiedOn: '2026-10-11',
  },
  {
    id: 'cloudflare',
    name: 'Cloudflare',
    kind: 'mcp',
    description: 'Your Cloudflare account through the full API via its hosted MCP server',
    logo: 'cloudflare',
    brandColor: '#F38020',
    endpoint: 'https://mcp.cloudflare.com/mcp',
    authMode: 'oauth',
    tokenHint: 'A Cloudflare API token also works as the bearer token for unattended use.',
    docsURL: 'https://developers.cloudflare.com/agents/model-context-protocol/mcp-servers-for-cloudflare/',
    verifiedOn: '2026-10-11',
  },
  {
    id: 'sentry',
    name: 'Sentry',
    kind: 'mcp',
    description: 'Issues, errors and traces via Sentry’s hosted MCP server',
    logo: 'sentry',
    brandColor: '#362D59',
    endpoint: 'https://mcp.sentry.dev/mcp',
    authMode: 'oauth',
    docsURL: 'https://docs.sentry.io/ai/mcp/',
    verifiedOn: '2026-10-11',
  },
  {
    id: 'stripe',
    name: 'Stripe',
    kind: 'mcp',
    description: 'Customers, payments, invoices and subscriptions via Stripe’s hosted MCP server',
    logo: 'stripe',
    brandColor: '#635BFF',
    endpoint: 'https://mcp.stripe.com',
    authMode: 'oauth',
    tokenHint: 'An agent API key also works as the bearer token for unattended use.',
    docsURL: 'https://docs.stripe.com/mcp',
    verifiedOn: '2026-10-11',
  },
  {
    id: 'custom-mcp',
    name: 'Custom MCP server',
    kind: 'mcp',
    description: 'Any streamable-HTTP MCP endpoint',
    brandColor: '#4B5563',
    endpoint: '',
    endpointHint: 'The server’s streamable-HTTP endpoint, e.g. https://mcp.example.com/mcp',
    tokenHint: 'Optional. Leave empty for servers without auth.',
  },
]

// Fallback for connectors that predate a preset removal / don't match
// any current preset — keeps ConnectorLogo/name rendering safe without
// needing a dummy entry in the list above.
const unknownPreset: ConnectorPreset = {
  id: 'unknown',
  name: 'Custom',
  kind: 'mcp',
  description: '',
  brandColor: '#4B5563',
}

// matchesPreset distinguishes presets sharing a kind (e.g. Gmail vs
// Google Calendar, both kind=google) by their OAuth scopes; kinds with
// only one preset match on kind alone.
function matchesPreset(
  c: { kind: string; config: Record<string, unknown> },
  p: ConnectorPreset,
): boolean {
  if (p.kind !== c.kind) return false
  if (c.kind === 'google' || c.kind === 'microsoft') {
    const scopes = JSON.stringify(c.config.scopes ?? '')
    return p.scopes?.every((s) => scopes.includes(s)) ?? false
  }
  if (
    c.kind === 'github' ||
    c.kind === 'imap' ||
    c.kind === 'caldav' ||
    c.kind === 'aws' ||
    c.kind === 'gcp' ||
    c.kind === 'bitbucket' ||
    c.kind === 'gitlab'
  ) {
    return true
  }
  const endpoint = String(c.config.endpoint ?? '')
  return !!p.endpoint && endpoint.startsWith(p.endpoint)
}

// presetFor resolves a connector to its friendly preset (name, logo,
// description) by kind + scopes, falling back to unknownPreset.
export function presetFor(c: { kind: string; config: Record<string, unknown> }): ConnectorPreset {
  const match = connectorPresets.find((p) => matchesPreset(c, p))
  if (match) return match
  // An mcp connector whose endpoint matches no catalog entry was added
  // through the custom tile (or predates one), so it renders as that.
  if (c.kind === 'mcp') return connectorPresets.find((p) => p.id === 'custom-mcp') ?? unknownPreset
  return unknownPreset
}
