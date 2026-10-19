import { describe, expect, it } from 'vitest'
import type { AdminProvider } from '../api/types'
import { matchPreset, providerPresets } from './providerPresets'

function provider(overrides: Partial<AdminProvider>): AdminProvider {
  return {
    id: 'p1',
    name: 'test',
    kind: 'api',
    driver: 'openaicompat',
    base_url: '',
    default_model: '',
    credential_ref: '',
    headers: {},
    enabled: true,
    ...overrides,
  }
}

describe('matchPreset', () => {
  it.each([
    'http://ollama:11434/v1',
    'http://localhost:11434/v1',
    'http://host.docker.internal:11434/v1',
  ])('matches the ollama preset by port for %s', (base_url) => {
    const p = provider({ base_url })
    expect(matchPreset(p).id).toBe('ollama')
  })

  it('matches by exact host when the port is not 11434', () => {
    const p = provider({ base_url: 'https://api.z.ai/api/paas/v4' })
    expect(matchPreset(p).id).toBe('glm')
  })

  it('falls back to custom for an unknown url', () => {
    const p = provider({ base_url: 'https://example.com/v1' })
    expect(matchPreset(p).id).toBe('custom')
  })

  it('matches the bedrock preset by driver', () => {
    const p = provider({ driver: 'bedrock', base_url: 'us-east-1' })
    expect(matchPreset(p).id).toBe('bedrock')
  })

  it('matches the anthropic preset by driver', () => {
    const p = provider({ driver: 'anthropic', base_url: '' })
    expect(matchPreset(p).id).toBe('anthropic')
  })

  it('offers no laya preset', () => {
    expect(providerPresets.map((x) => x.id)).not.toContain('laya')
  })

  it('falls back to custom for a row with the removed laya driver', () => {
    const p = provider({ driver: 'laya', base_url: 'http://laya:8000' })
    expect(matchPreset(p).id).toBe('custom')
  })

  it('matches the jev preset by driver', () => {
    const p = provider({ driver: 'jev', base_url: 'https://api.typesafe.ai' })
    expect(matchPreset(p).id).toBe('jev')
  })

  it('matches the anthropic preset for a kind=cli row regardless of base_url', () => {
    const p = provider({ kind: 'cli', driver: 'claude-cli', base_url: '' })
    expect(matchPreset(p).id).toBe('anthropic')
  })

  it('matches the cursor preset for a kind=cli cursor-cli row', () => {
    const p = provider({ kind: 'cli', driver: 'cursor-cli', base_url: '' })
    expect(matchPreset(p).id).toBe('cursor')
  })
})

describe('openrouter preset (D-163)', () => {
  it('matches a connected row by host', () => {
    const p = provider({ base_url: 'https://openrouter.ai/api/v1' })
    expect(matchPreset(p).id).toBe('openrouter')
  })
})

describe('cliproxy presets', () => {
  it('matches the chatgpt preset by port 8317', () => {
    const p = provider({ base_url: 'http://cliproxy:8317/v1' })
    const preset = matchPreset(p)
    expect(preset.id).toBe('chatgpt')
    expect(preset.name).toBe('OpenAI Subscription')
  })

  it('matches OpenAI Subscription by name', () => {
    const p = provider({ name: 'OpenAI Subscription', base_url: 'http://cliproxy:8317/v1' })
    expect(matchPreset(p).id).toBe('chatgpt')
  })

  it('matches the claude preset by port 8317 when name or model refers to claude', () => {
    const p = provider({ name: 'Claude Subscription', base_url: 'http://cliproxy:8317/v1' })
    const preset = matchPreset(p)
    expect(preset.id).toBe('claude')
    expect(preset.name).toBe('Claude Subscription')
  })

  it('matches a cliproxy row whose model is sonnet as claude', () => {
    const p = provider({ name: 'Work', default_model: 'sonnet', base_url: 'http://cliproxy:8317/v1' })
    expect(matchPreset(p).id).toBe('claude')
  })

  it('matches a cliproxy o-series model as the openai subscription', () => {
    const p = provider({ name: 'Work', default_model: 'o4-mini', base_url: 'http://cliproxy:8317/v1' })
    expect(matchPreset(p).id).toBe('chatgpt')
  })
})

