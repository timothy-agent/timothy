import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
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

function renderPage(initialEntry = '/settings') {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Settings />
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  // jsdom lacks scrollIntoView; Radix Select calls it on open.
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listProviders).mockResolvedValue([openaiProvider])
  vi.mocked(providersHealth).mockResolvedValue([
    { name: 'OpenAI', enabled: true, healthy: true },
  ])
  vi.mocked(secretStatus).mockResolvedValue({ configured: true, backend: 'db' })
  vi.mocked(getSecretBackendConfig).mockResolvedValue({})
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

describe('Settings tabs', () => {
  it('URL-syncs the active tab', async () => {
    renderPage('/settings?tab=secrets')
    expect(await screen.findByText('HashiCorp Vault')).toBeTruthy()
    expect(screen.queryByText('Connect a provider')).toBeNull()
  })

  it('defaults to Agents', async () => {
    renderPage()
    expect(await screen.findByText('general')).toBeTruthy()
    expect(screen.getByText(/who serves a session/)).toBeTruthy()
  })
})

describe('Features tab agent settings', () => {
  it('renders stored runtime values and patches them on save', async () => {
    vi.mocked(getSettings).mockResolvedValue({
      settings: { tools_enabled: true },
      values: { session_token_budget: '120000', skills_allowlist: '' },
    })
    vi.mocked(usageBudget).mockResolvedValue({
      day: { limit_usd: null, spend_usd: 0, over: false },
      month: { limit_usd: null, spend_usd: 0, over: false },
    })
    vi.mocked(patchSettingValues).mockResolvedValue()

    renderPage('/settings?tab=features')
    const budget = await screen.findByLabelText('Session token budget')
    expect((budget as HTMLInputElement).value).toBe('120000')

    fireEvent.change(screen.getByLabelText('Skills allowlist'), {
      target: { value: 'coding-task, research' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchSettingValues).toHaveBeenCalledWith({
        session_token_budget: '120000',
        skills_allowlist: 'coding-task, research',
      }),
    )
  })
})

describe('Secrets tab default backend', () => {
  it('shows the built-in storage card carrying the default badge', async () => {
    renderPage('/settings?tab=secrets')
    expect(await screen.findByText('Timothy storage')).toBeTruthy()
    expect(screen.getByText('default')).toBeTruthy()
    // Unconfigured backends cannot claim the default.
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

    renderPage('/settings?tab=secrets')
    const enabled = (await screen.findAllByRole('button', { name: 'Make default' })).find(
      (b) => !(b as HTMLButtonElement).disabled,
    )
    fireEvent.click(enabled!)
    await waitFor(() => expect(setDefaultSecretBackend).toHaveBeenCalledWith('vault'))
  })
})

describe('Providers tab', () => {
  it('renders configured cards and the preset tile grid', async () => {
    renderPage('/settings?tab=providers')
    expect(await screen.findByText('Your providers · 1')).toBeTruthy()
    expect(screen.getByText('healthy')).toBeTruthy()
    // Every preset is offered as a tile.
    for (const name of ['AWS Bedrock', 'Groq', 'Mistral', 'OpenRouter', 'Ollama', 'Custom endpoint']) {
      expect(screen.getByRole('button', { name: new RegExp(name) })).toBeTruthy()
    }
  })

  it('opens a preset-aware dialog: bedrock asks for region, not a key', async () => {
    renderPage('/settings?tab=providers')
    fireEvent.click(await screen.findByRole('button', { name: /AWS Bedrock/ }))
    const dialog = within(await screen.findByRole('dialog'))
    expect(dialog.getByText('Connect AWS Bedrock')).toBeTruthy()
    expect(dialog.getByText('Region')).toBeTruthy()
    expect(dialog.getByText('AWS profile (optional)')).toBeTruthy()
    // The card behind the dialog has a key editor; the bedrock dialog must not.
    expect(dialog.queryByText('API key')).toBeNull()
  })

  it('stores the key, validates, then creates enabled with the model declared', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(validateProvider).mockResolvedValue({ ok: true, latency_ms: 187, model: 'llama-3.3-70b-versatile' })
    vi.mocked(createProvider).mockResolvedValue('p2')

    renderPage('/settings?tab=providers')
    fireEvent.click(await screen.findByRole('button', { name: /Groq/ }))
    fireEvent.change(screen.getByPlaceholderText('gsk_…'), { target: { value: ' gsk_abc\u200B ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Validate & add' }))

    await waitFor(() => expect(createProvider).toHaveBeenCalled())
    // Pasted key is stripped of whitespace/zero-width chars.
    expect(setSecret).toHaveBeenCalledWith('GROQ_API_KEY', 'gsk_abc')
    expect(validateProvider).toHaveBeenCalledWith(
      expect.objectContaining({ driver: 'openaicompat', base_url: 'https://api.groq.com/openai/v1' }),
      'llama-3.3-70b-versatile',
    )
    expect(createProvider).toHaveBeenCalledWith(
      expect.objectContaining({
        enabled: true,
        default_model: 'llama-3.3-70b-versatile',
        models: [{ id: 'llama-3.3-70b-versatile' }],
      }),
    )
  })

  it('asks for a reference, not a key, when the default backend is external', async () => {
    vi.mocked(listAgents).mockResolvedValue([
    { id: 'a1', name: 'general', description: 'Everyday', prompt_overlay: '', route: '', skills: [], tools: [], memory: true, is_default: true, enabled: true },
  ])
  vi.mocked(listRoutes).mockResolvedValue([])
  vi.mocked(listSecretBackends).mockResolvedValue([
      { backend: 'db', configured: true, default: false },
      { backend: 'vault', configured: true, default: true },
      { backend: 'asm', configured: false, default: false },
    ])

    renderPage('/settings?tab=providers')
    fireEvent.click(await screen.findByRole('button', { name: /Groq/ }))
    const dialog = within(await screen.findByRole('dialog'))
    const input = await dialog.findByPlaceholderText(/Vault path/)
    // A reference is not a secret: no password masking.
    expect((input as HTMLInputElement).type).toBe('text')
    expect(dialog.getByText(/paste the path of the secret/)).toBeTruthy()
  })

  it('keeps the dialog open and skips create when validation fails', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(validateProvider).mockResolvedValue({
      ok: false,
      latency_ms: 5000,
      model: 'llama-3.3-70b-versatile',
      detail: 'upstream status 401',
    })

    renderPage('/settings?tab=providers')
    fireEvent.click(await screen.findByRole('button', { name: /Groq/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Validate & add' }))

    expect(await screen.findByText(/Validation failed after 5000 ms/)).toBeTruthy()
    expect(createProvider).not.toHaveBeenCalled()
  })
})

describe('Models editor', () => {
  it('adds a model from the provider listing and patches the row', async () => {
    vi.mocked(availableModels).mockResolvedValue([{ id: 'gpt-4o' }, { id: 'o3-mini' }])
    vi.mocked(patchProvider).mockResolvedValue()

    renderPage('/settings?tab=providers')
    fireEvent.click(await screen.findByRole('button', { name: '+ add model' }))
    // Declared models are filtered out of the choices.
    const picker = await screen.findByRole('combobox', { name: 'available models' })
    fireEvent.click(picker)
    expect(screen.queryByRole('option', { name: 'gpt-4o' })).toBeNull()
    fireEvent.click(await screen.findByRole('option', { name: 'o3-mini' }))
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', {
        models: [{ id: 'gpt-4o' }, { id: 'gpt-4o-mini', context_window: 128000 }, { id: 'o3-mini' }],
        default_model: 'gpt-4o',
      }),
    )
  })

  it('falls back to manual entry when the driver cannot list models', async () => {
    vi.mocked(availableModels).mockRejectedValue(
      new ChatError(422, 'driver bedrock cannot list models', 'unsupported'),
    )
    vi.mocked(patchProvider).mockResolvedValue()

    renderPage('/settings?tab=providers')
    fireEvent.click(await screen.findByRole('button', { name: '+ add model' }))
    expect(await screen.findByText(/doesn’t list its models/)).toBeTruthy()
    fireEvent.change(screen.getByLabelText('model id'), { target: { value: 'my-model' } })
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

    renderPage('/settings?tab=providers')
    fireEvent.click(await screen.findByRole('button', { name: 'Remove gpt-4o' }))

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', {
        models: [{ id: 'gpt-4o-mini', context_window: 128000 }],
        default_model: 'gpt-4o-mini',
      }),
    )
  })
})
