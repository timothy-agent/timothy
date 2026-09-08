import axe from 'axe-core'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminProvider } from '../api/types'
import { TooltipProvider } from '../components/ui/tooltip'
import { Settings } from './Settings'

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    availableModels: vi.fn(),
    createProvider: vi.fn(),
    deleteProvider: vi.fn(),
    deleteSecret: vi.fn(),
    deleteSecretBackendConfig: vi.fn(),
    getSecretBackendConfig: vi.fn(),
    getSettings: vi.fn(),
    patchSettingValues: vi.fn(),
    createAgent: vi.fn(),
    deleteAgent: vi.fn(),
    listAgents: vi.fn(),
    listConnectors: vi.fn(),
    listDestinations: vi.fn(),
    listProviders: vi.fn(),
    listRoutes: vi.fn(),
    listSecretBackends: vi.fn(),
    listSecretRefs: vi.fn(),
    migrateAllSecrets: vi.fn(),
    patchAgent: vi.fn(),
    patchConnector: vi.fn(),
    patchDestination: vi.fn(),
    patchRoute: vi.fn(),
    patchSettings: vi.fn(),
    providersHealth: vi.fn(),
    putSecretBackendConfig: vi.fn(),
    searchCatalog: vi.fn(),
    secretStatus: vi.fn(),
    setDefaultAgent: vi.fn(),
    setDefaultSecretBackend: vi.fn(),
    setSecret: vi.fn(),
    testConnector: vi.fn(),
    testDestination: vi.fn(),
    testProvider: vi.fn(),
    testSecretBackend: vi.fn(),
    usageBudget: vi.fn(),
    validateProvider: vi.fn(),
  }
})

import {
  availableModels,
  getSecretBackendConfig,
  getSettings,
  listAgents,
  listConnectors,
  listDestinations,
  listProviders,
  listRoutes,
  listSecretBackends,
  listSecretRefs,
  providersHealth,
  secretStatus,
} from '../api/client'

const openaiProvider: AdminProvider = {
  id: 'p1',
  name: 'OpenAI',
  kind: 'api',
  driver: 'openaicompat',
  base_url: 'https://api.openai.com/v1',
  default_model: 'gpt-4o',
  credential_ref: 'OPENAI_API_KEY',
  headers: {},
  enabled: true,
}

function renderPage(initialEntry: string) {
  return render(
    <TooltipProvider>
      <MemoryRouter initialEntries={[initialEntry]}>
        <Routes>
          <Route path="/settings/*" element={<Settings />} />
        </Routes>
      </MemoryRouter>
    </TooltipProvider>,
  )
}

const axeOptions = { rules: { 'color-contrast': { enabled: false } } }

afterEach(cleanup)
beforeEach(() => {
  // jsdom lacks scrollIntoView; Radix Select calls it on open.
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listProviders).mockResolvedValue([openaiProvider])
  vi.mocked(availableModels).mockResolvedValue([])
  vi.mocked(providersHealth).mockResolvedValue([
    { name: 'OpenAI', enabled: true, healthy: true },
  ])
  vi.mocked(listRoutes).mockResolvedValue([])
  vi.mocked(listConnectors).mockResolvedValue([])
  vi.mocked(listDestinations).mockResolvedValue([])
  vi.mocked(listAgents).mockResolvedValue([])
  vi.mocked(listSecretRefs).mockResolvedValue([])
  vi.mocked(listSecretBackends).mockResolvedValue([
    { backend: 'db', configured: true, default: true },
    { backend: 'vault', configured: false, default: false },
    { backend: 'asm', configured: false, default: false },
  ])
  vi.mocked(getSettings).mockResolvedValue({ settings: {}, values: {} })
  vi.mocked(getSecretBackendConfig).mockResolvedValue({})
  vi.mocked(secretStatus).mockResolvedValue({ configured: true, backend: 'db' })
})

function expectSingleH1() {
  expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
}

describe('Settings pages accessibility', () => {
  it('has no axe violations on the providers page', async () => {
    const { container } = renderPage('/settings/providers')
    await screen.findByText('Your providers · 1')
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })

  it('has no axe violations on the routes page', async () => {
    const { container } = renderPage('/settings/routes')
    await screen.findByRole('heading', { level: 1 })
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })

  it('has no axe violations on the connectors page', async () => {
    const { container } = renderPage('/settings/connectors')
    await screen.findByRole('heading', { level: 1 })
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })

  it('has no axe violations on the destinations page', async () => {
    const { container } = renderPage('/settings/destinations')
    await screen.findByRole('heading', { level: 1 })
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })

  it('has no axe violations on the agents page', async () => {
    const { container } = renderPage('/settings/agents')
    await screen.findByRole('heading', { level: 1 })
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })

  it('has no axe violations on the credentials page', async () => {
    vi.mocked(listSecretRefs).mockResolvedValue([
      { name: 'OPENAI_API_KEY', backend: 'db', referenced_by: [], system: false },
    ])
    const { container } = renderPage('/settings/credentials')
    await screen.findByText('OPENAI_API_KEY')
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })

  it('has no axe violations on the secrets page', async () => {
    const { container } = renderPage('/settings/secrets')
    await screen.findByText('HashiCorp Vault')
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })

  it('has no axe violations on the features page', async () => {
    vi.mocked(getSettings).mockResolvedValue({
      settings: { tools_enabled: true },
      values: { sensitive_tool_route: '', timezone: '' },
    })
    const { container } = renderPage('/settings/features')
    await screen.findByRole('region', { name: 'Timezone' })
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })
})
