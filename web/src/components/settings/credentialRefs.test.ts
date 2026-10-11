import { describe, expect, it } from 'vitest'
import { mcpOAuthRefs, refBaseFor, tokenRefFor } from './credentialRefs'

describe('refBaseFor', () => {
  it.each([
    ['notion', 'NOTION'],
    ['github-mcp', 'GITHUB_MCP'],
    ['My Server', 'MY_SERVER'],
    ['  work.mail  ', 'WORK_MAIL'],
  ])('%s -> %s', (name, want) => {
    expect(refBaseFor(name)).toBe(want)
  })
})

describe('tokenRefFor', () => {
  it.each([
    ['github', 'GITHUB', 'GITHUB_PAT'],
    ['github', 'WORK', 'WORK_GITHUB_PAT'],
    ['github', 'GITHUB_MCP', 'GITHUB_MCP_GITHUB_PAT'],
    ['bitbucket', 'BITBUCKET', 'BITBUCKET_TOKEN'],
    ['bitbucket', 'WORK', 'WORK_BITBUCKET_TOKEN'],
    ['gitlab', 'GITLAB', 'GITLAB_TOKEN'],
    ['gitlab', 'WORK', 'WORK_GITLAB_TOKEN'],
    ['imap', 'FASTMAIL', 'FASTMAIL_IMAP_PASSWORD'],
    ['caldav', 'ICLOUD', 'ICLOUD_CALDAV_PASSWORD'],
    ['mcp', 'NOTION', 'NOTION_MCP_TOKEN'],
    ['mcp', 'GITHUB_MCP', 'GITHUB_MCP_TOKEN'],
    ['mcp', 'MY_SERVER', 'MY_SERVER_MCP_TOKEN'],
    ['aws', 'PROD', 'PROD_MCP_TOKEN'],
  ])('%s %s -> %s', (kind, refBase, want) => {
    expect(tokenRefFor(kind, refBase)).toBe(want)
  })

  it('names one connector the same from a raw name on either MCP page', () => {
    expect(tokenRefFor('mcp', refBaseFor('notion-mcp'))).toBe('NOTION_MCP_TOKEN')
    expect(tokenRefFor('mcp', refBaseFor('My Server'))).toBe('MY_SERVER_MCP_TOKEN')
  })
})

describe('mcpOAuthRefs', () => {
  it.each([
    ['NOTION', 'NOTION_MCP_OAUTH', 'NOTION_MCP_CLIENT_SECRET'],
    ['GITHUB_MCP', 'GITHUB_MCP_OAUTH', 'GITHUB_MCP_CLIENT_SECRET'],
  ])('%s', (refBase, tokens, clientSecret) => {
    expect(mcpOAuthRefs(refBase)).toEqual({ tokens, clientSecret })
  })
})
