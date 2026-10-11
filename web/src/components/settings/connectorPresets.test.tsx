import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { connectorPresets, presetFor } from './connectorPresets'
import { ConnectorLogoSprite } from './ConnectorLogo'

// catalog is every hosted MCP server tile (issue #1162): an mcp preset
// with a verified endpoint, as opposed to the custom tile.
const catalog = connectorPresets.filter((p) => p.kind === 'mcp' && p.verifiedOn)

describe('connectorPresets', () => {
  it('lists the custom MCP server tile last', () => {
    const last = connectorPresets[connectorPresets.length - 1]
    expect(last.id).toBe('custom-mcp')
    expect(last.kind).toBe('mcp')
    expect(last.endpoint).toBe('')
  })

  it('matches an mcp connector with a known endpoint to its catalog tile', () => {
    const p = presetFor({ kind: 'mcp', config: { endpoint: 'https://api.githubcopilot.com/mcp/' } })
    expect(p.id).toBe('github')
  })

  it('renders an mcp connector with an unknown endpoint as the custom tile', () => {
    const p = presetFor({ kind: 'mcp', config: { endpoint: 'https://mcp.example.com/mcp' } })
    expect(p.id).toBe('custom-mcp')
    expect(p.name).toBe('Custom MCP server')
  })

  it('keeps the unknown fallback for kinds with no tile', () => {
    const p = presetFor({ kind: 'nonexistent', config: {} })
    expect(p.id).toBe('unknown')
  })
})

describe('hosted MCP catalog', () => {
  it('lists the planned servers', () => {
    expect(catalog.map((p) => p.id)).toEqual([
      'notion',
      'slack',
      'linear',
      'atlassian',
      'hubspot',
      'cloudflare',
      'sentry',
      'stripe',
    ])
  })

  it.each(catalog.map((p) => [p.id, p] as const))('%s has an https endpoint, auth mode, docs link and check date', (_id, p) => {
    expect(p.endpoint).toMatch(/^https:\/\/[^\s]+$/)
    expect(p.authMode).toBe('oauth')
    expect(p.docsURL).toMatch(/^https:\/\//)
    expect(p.verifiedOn).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(Number.isNaN(Date.parse(p.verifiedOn ?? ''))).toBe(false)
    expect(p.description).not.toBe('')
  })

  it.each(catalog.map((p) => [p.id, p] as const))('%s has a logo symbol in the sprite', (_id, p) => {
    const { container } = render(<ConnectorLogoSprite />)
    expect(p.logo).toBeTruthy()
    expect(container.querySelector(`symbol#clogo-${p.logo}`)).not.toBeNull()
    expect(p.brandColor).toMatch(/^#[0-9A-Fa-f]{6}$/)
  })

  it.each(catalog.map((p) => [p.id, p] as const))('%s resolves a saved connector back to its tile', (_id, p) => {
    expect(presetFor({ kind: 'mcp', config: { endpoint: p.endpoint, auth_mode: 'oauth' } }).id).toBe(p.id)
  })

  it('keeps endpoints distinct so no tile shadows another', () => {
    const endpoints = catalog.map((p) => p.endpoint)
    expect(new Set(endpoints).size).toBe(endpoints.length)
  })
})
