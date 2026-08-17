import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChatError } from '../api/client'
import type { AdminProvider } from '../api/types'
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
    listProviders: vi.fn(),
    listRoutes: vi.fn(),
    patchAgent: vi.fn(),
    setDefaultAgent: vi.fn(),
    listSecretBackends: vi.fn(),
    setDefaultSecretBackend: vi.fn(),
    patchBudget: vi.fn(),
    patchProvider: vi.fn(),
    patchRoute: vi.fn(),
    patchSettings: vi.fn(),
    providersHealth: vi.fn(),
    putSecretBackendConfig: vi.fn(),
    searchCatalog: vi.fn(),
    secretStatus: vi.fn(),
    setSecret: vi.fn(),
    testProvider: vi.fn(),
    testSecretBackend: vi.fn(),
    usageBudget: vi.fn(),
    validateProvider: vi.fn(),
  }
})

import {
  availableModels,
  createProvider,
  getSecretBackendConfig,
  getSettings,
  listAgents,
  listProviders,
  listRoutes,
  listSecretBackends,
  patchProvider,
  patchSettingValues,
  providersHealth,
  searchCatalog,
  secretStatus,
  setDefaultSecretBackend,
  setSecret,
  usageBudget,
  validateProvider,
} from '../api/client'

const openaiProvider: AdminProvider = {
  id: 'p1',
  name: 'OpenAI',
  kind: 'api',
  driver: 'openaicompat',
  base_url: 'https://api.openai.com/v1',
  default_model: 'gpt-4o',
  models: [{ id: 'gpt-4o' }, { id: 'gpt-4o-mini', context_window: 128000 }],
  credential_ref: 'OPENAI_API_KEY',
  headers: {},
  enabled: true,
}

function renderPage(initialEntry = '/settings/providers') {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route path="/settings/*" element={<Settings />} />
      </Routes>
    </MemoryRouter>,
  )
}

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
  vi.mocked(secretStatus).mockResolvedValue({ configured: true, backend: 'db' })
  vi.mocked(getSecretBackendConfig).mockResolvedValue({})
  vi.mocked(searchCatalog).mockResolvedValue([])
  vi.mocked(listAgents).mockResolvedValue([
    { id: 'a1', name: 'general', description: 'Everyday', prompt_overlay: '', route: '', skills: [], tools: [], memory: true, is_default: true, enabled: true },
  ])
  vi.mocked(listRoutes).mockResolvedValue([])
  vi.mocked(listSecretBackends).mockResolvedValue([
    { backend: 'db', configured: true, default: true },
    { backend: 'vault', configured: false, default: false },
    { backend: 'asm', configured: false, default: false },
  ])
})

describe('Settings pages', () => {
  it('renders the area as its own page with a heading', async () => {
    renderPage('/settings/secrets')
    expect(await screen.findByText('HashiCorp Vault')).toBeTruthy()
    expect(screen.getByRole('heading', { name: 'Secrets' })).toBeTruthy()
    expect(screen.queryByText('Your providers')).toBeNull()
  })

  it('redirects /settings to providers', async () => {
    renderPage('/settings')
    expect(await screen.findByText('healthy')).toBeTruthy()
    expect(screen.getByRole('heading', { name: 'Providers' })).toBeTruthy()
  })
})

describe('Features tab sensitive tool route', () => {
  beforeEach(() => {
    vi.mocked(getSettings).mockResolvedValue({
      settings: { tools_enabled: true },
      values: { sensitive_tool_route: '' },
    })
    vi.mocked(usageBudget).mockResolvedValue({
      day: { currency: '', limit: null, spend: 0, over: false },
      month: { currency: '', limit: null, spend: 0, over: false },
    })
    vi.mocked(patchSettingValues).mockResolvedValue()
  })

  it('offers every route plus Off, and patches the chosen one', async () => {
    vi.mocked(listRoutes).mockResolvedValue([
      { name: 'default', chain: [], strategy: 'ordered', enabled: true },
      { name: 'local', chain: [], strategy: 'ordered', enabled: true },
    ])

    renderPage('/settings/features')
    const trigger = await screen.findByRole('combobox', { name: 'Sensitive tool route' })
    fireEvent.click(trigger)
    fireEvent.click(await screen.findByText('local'))

    const card = trigger.closest('div.rounded-xl') as HTMLElement
    fireEvent.click(within(card).getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchSettingValues).toHaveBeenCalledWith({ sensitive_tool_route: 'local' }),
    )
  })

  it('falls back to a text input when the route list fails to load', async () => {
    vi.mocked(listRoutes).mockRejectedValue(new Error('admin proxy unavailable'))

    renderPage('/settings/features')
    const input = await screen.findByLabelText('Sensitive tool route')
    expect((input as HTMLInputElement).tagName).toBe('INPUT')

    fireEvent.change(input, { target: { value: 'local' } })
    const card = input.closest('div.rounded-xl') as HTMLElement
    fireEvent.click(within(card).getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchSettingValues).toHaveBeenCalledWith({ sensitive_tool_route: 'local' }),
    )
  })
})

describe('Secrets tab default backend', () => {
  it('shows the built-in storage card carrying the default badge', async () => {
    renderPage('/settings/secrets')
    expect(await screen.findByText('Timothy storage')).toBeTruthy()
    expect(screen.getByText('default')).toBeTruthy()
    // Unconfigured backends cannot claim the default: vault, asm.
    const buttons = screen.getAllByRole('button', { name: 'Make default' })
    expect(buttons.length).toBe(2)
    for (const b of buttons) expect((b as HTMLButtonElement).disabled).toBe(true)
  })

  it('claims the default for a configured backend', async () => {
    vi.mocked(getSecretBackendConfig).mockResolvedValue({ address: 'http://vault:8200' })
    vi.mocked(listAgents).mockResolvedValue([
      { id: 'a1', name: 'general', description: 'Everyday', prompt_overlay: '', route: '', skills: [], tools: [], memory: true, is_default: true, enabled: true },
    ])
    vi.mocked(listRoutes).mockResolvedValue([])
    vi.mocked(listSecretBackends).mockResolvedValue([
      { backend: 'db', configured: true, default: true },
      { backend: 'vault', configured: true, default: false },
      { backend: 'asm', configured: false, default: false },
    ])
    vi.mocked(setDefaultSecretBackend).mockResolvedValue()

    renderPage('/settings/secrets')
    const enabled = (await screen.findAllByRole('button', { name: 'Make default' })).find(
      (b) => !(b as HTMLButtonElement).disabled,
    )
    fireEvent.click(enabled!)
    await waitFor(() => expect(setDefaultSecretBackend).toHaveBeenCalledWith('vault'))
  })
})

describe('Providers tab', () => {
  it('renders configured cards and the preset tile grid', async () => {
    renderPage('/settings/providers')
    expect(await screen.findByText('Your providers · 1')).toBeTruthy()
    expect(screen.getByText('healthy')).toBeTruthy()
    // Every preset is offered as a tile.
    for (const name of ['AWS Bedrock', 'GLM (Z.ai)', 'Grok (xAI)', 'Ollama', 'Custom endpoint']) {
      expect(screen.getByRole('button', { name: new RegExp(name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')) })).toBeTruthy()
    }
  })

  it('navigates to its own add page: bedrock asks for a region dropdown and a key', async () => {
    renderPage('/settings/providers')
    fireEvent.click(await screen.findByRole('button', { name: /AWS Bedrock/ }))
    expect(await screen.findByText('Add AWS Bedrock')).toBeTruthy()
    expect(screen.getByText('Region')).toBeTruthy()
    expect(screen.getByRole('combobox')).toBeTruthy()
    // Static keys are the only bedrock auth now: access key id + secret
    // access key are collected directly, not a generic API key field.
    expect(screen.getByText('Access Key ID')).toBeTruthy()
    expect(screen.getByText('Secret Access Key')).toBeTruthy()
  })

  it('disables Add until a test passes, then stores the key and creates enabled', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(validateProvider).mockResolvedValue({ ok: true, latency_ms: 187, model: 'glm-4.7-flash' })
    vi.mocked(createProvider).mockResolvedValue('p2')
    // glm-4.7-flash is Z.ai's free tier in the synced catalog.
    vi.mocked(searchCatalog).mockResolvedValue([
      {
        id: 'glm-4.7-flash',
        model_key: 'zai/glm-4.7-flash',
        litellm_provider: 'zai',
        mode: 'chat',
        input_per_mtok: 0,
        output_per_mtok: 0,
      },
    ])

    renderPage('/settings/providers')
    fireEvent.click(await screen.findByRole('button', { name: /GLM/ }))
    const addButton = await screen.findByRole('button', { name: 'Add provider' })
    expect((addButton as HTMLButtonElement).disabled).toBe(true)

    fireEvent.change(screen.getByLabelText(/API key/), { target: { value: ' gsk_abc​ ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    await waitFor(() => expect((addButton as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(addButton)

    await waitFor(() => expect(createProvider).toHaveBeenCalled())
    // Pasted key is stripped of whitespace/zero-width chars.
    expect(setSecret).toHaveBeenCalledWith('ZAI_API_KEY', 'gsk_abc')
    expect(validateProvider).toHaveBeenCalledWith(
      expect.objectContaining({ driver: 'openaicompat', base_url: 'https://api.z.ai/api/paas/v4' }),
      'glm-4.7-flash',
    )
    expect(createProvider).toHaveBeenCalledWith(
      expect.objectContaining({
        enabled: true,
        default_model: 'glm-4.7-flash',
        models: [{ id: 'glm-4.7-flash', prices: { input_per_mtok: 0, output_per_mtok: 0 } }],
      }),
    )
  })

  it('shows an inline error when testing without a key', async () => {
    renderPage('/settings/providers')
    fireEvent.click(await screen.findByRole('button', { name: /GLM/ }))
    fireEvent.click(await screen.findByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText(/An API key is required to test this provider/)).toBeTruthy()
    expect(validateProvider).not.toHaveBeenCalled()
  })

  it('re-locks Add after editing a field past a passing test', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(validateProvider).mockResolvedValue({ ok: true, latency_ms: 187, model: 'glm-4.7-flash' })

    renderPage('/settings/providers')
    fireEvent.click(await screen.findByRole('button', { name: /GLM/ }))
    fireEvent.change(screen.getByLabelText(/API key/), { target: { value: 'gsk_abc' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    const addButton = await screen.findByRole('button', { name: 'Add provider' })
    await waitFor(() => expect((addButton as HTMLButtonElement).disabled).toBe(false))

    fireEvent.change(screen.getByLabelText(/API key/), { target: { value: 'gsk_changed' } })
    expect((addButton as HTMLButtonElement).disabled).toBe(true)
  })

  it('still asks for the raw key, and names Vault as its destination, when the default backend is external', async () => {
    vi.mocked(listAgents).mockResolvedValue([
      { id: 'a1', name: 'general', description: 'Everyday', prompt_overlay: '', route: '', skills: [], tools: [], memory: true, is_default: true, enabled: true },
    ])
    vi.mocked(listRoutes).mockResolvedValue([])
    vi.mocked(listSecretBackends).mockResolvedValue([
      { backend: 'db', configured: true, default: false },
      { backend: 'vault', configured: true, default: true },
      { backend: 'asm', configured: false, default: false },
    ])

    renderPage('/settings/providers')
    fireEvent.click(await screen.findByRole('button', { name: /GLM/ }))
    const input = await screen.findByPlaceholderText('paste key')
    // Every backend takes the raw key now, still masked.
    expect((input as HTMLInputElement).type).toBe('password')
    expect(screen.getByText(/Timothy stores the key in Vault \(path timothy\/ZAI_API_KEY\)/)).toBeTruthy()
  })

  it('keeps Add disabled and skips create when validation fails', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(validateProvider).mockResolvedValue({
      ok: false,
      latency_ms: 5000,
      model: 'glm-4.7-flash',
      detail: 'upstream status 401',
    })

    renderPage('/settings/providers')
    fireEvent.click(await screen.findByRole('button', { name: /GLM/ }))
    fireEvent.change(screen.getByLabelText(/API key/), { target: { value: 'gsk_abc' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    expect(await screen.findByText(/Failed after 5000 ms/)).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Add provider' }) as HTMLButtonElement).disabled).toBe(true)
    expect(createProvider).not.toHaveBeenCalled()
  })
})

describe('Provider manage page', () => {
  it('navigates to the manage page from the card', async () => {
    renderPage('/settings/providers')
    fireEvent.click(await screen.findByRole('button', { name: 'Manage' }))
    expect(await screen.findByRole('heading', { name: 'OpenAI' })).toBeTruthy()
  })

  it('adds a model by id and patches the row', async () => {
    vi.mocked(availableModels).mockResolvedValue([{ id: 'gpt-4o' }, { id: 'o3-mini' }])
    vi.mocked(patchProvider).mockResolvedValue()

    renderPage('/settings/providers/p1')
    fireEvent.change(await screen.findByPlaceholderText('model id'), { target: { value: 'o3-mini' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', {
        models: [{ id: 'gpt-4o' }, { id: 'gpt-4o-mini', context_window: 128000 }, { id: 'o3-mini' }],
        default_model: 'gpt-4o',
      }),
    )
  })

  it('offers ids from the provider listing as suggestions, minus already-declared ones', async () => {
    vi.mocked(availableModels).mockResolvedValue([{ id: 'gpt-4o' }, { id: 'o3-mini' }])

    renderPage('/settings/providers/p1')
    fireEvent.focus(await screen.findByPlaceholderText('model id'))
    expect(await screen.findByRole('option', { name: /o3-mini/ })).toBeTruthy()
    expect(screen.queryByRole('option', { name: /^gpt-4o$/ })).toBeNull()
  })

  it('still allows manual entry when the driver cannot list models', async () => {
    vi.mocked(availableModels).mockRejectedValue(
      new ChatError(422, 'driver bedrock cannot list models', 'unsupported'),
    )
    vi.mocked(patchProvider).mockResolvedValue()

    renderPage('/settings/providers/p1')
    fireEvent.change(await screen.findByPlaceholderText('model id'), { target: { value: 'my-model' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith(
        'p1',
        expect.objectContaining({
          models: expect.arrayContaining([{ id: 'my-model' }]),
        }),
      ),
    )
  })

  it('removes a model and moves the default to the first survivor', async () => {
    vi.mocked(patchProvider).mockResolvedValue()

    renderPage('/settings/providers/p1')
    fireEvent.click(await screen.findByRole('button', { name: 'Remove gpt-4o' }))

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', {
        models: [{ id: 'gpt-4o-mini', context_window: 128000 }],
        default_model: 'gpt-4o-mini',
      }),
    )
  })
})
