import { describe, expect, it } from 'vitest'
import { connectorPresets, presetFor } from './connectorPresets'

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
