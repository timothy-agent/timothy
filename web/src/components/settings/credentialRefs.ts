import { slugify } from '../../lib/slugify'

// refBaseFor is the upper-snake prefix of every credential ref derived
// from a connector name.
export function refBaseFor(name: string): string {
  return slugify(name).toUpperCase().replace(/-/g, '_')
}

// tokenRefFor derives the token credential's ref name from the
// connector name. Shared by the preview and the submit path so the name
// shown is the name saved. Suffix without stuttering: a name already
// ending in the flavor word ("github", "github-mcp") gets the bare
// _PAT/_TOKEN suffix.
export function tokenRefFor(kind: string, refBase: string): string {
  switch (kind) {
    case 'github':
      return refBase.endsWith('GITHUB') ? `${refBase}_PAT` : `${refBase}_GITHUB_PAT`
    case 'bitbucket':
      return refBase.endsWith('BITBUCKET') ? `${refBase}_TOKEN` : `${refBase}_BITBUCKET_TOKEN`
    case 'gitlab':
      return refBase.endsWith('GITLAB') ? `${refBase}_TOKEN` : `${refBase}_GITLAB_TOKEN`
    case 'imap':
      return `${refBase}_IMAP_PASSWORD`
    case 'caldav':
      return `${refBase}_CALDAV_PASSWORD`
    default:
      return refBase.endsWith('_MCP') ? `${refBase}_TOKEN` : `${refBase}_MCP_TOKEN`
  }
}

// mcpOAuthRefs names an OAuth-mode MCP connector's token bundle and
// pasted client secret, with the same no-stutter rule.
export function mcpOAuthRefs(refBase: string): { tokens: string; clientSecret: string } {
  const base = refBase.endsWith('_MCP') ? refBase : `${refBase}_MCP`
  return { tokens: `${base}_OAUTH`, clientSecret: `${base}_CLIENT_SECRET` }
}
