import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminProvider } from '../../api/types'
import { buildPatch, ProviderEdit } from './ProviderEdit'

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

vi.mock('../../api/client', () => ({
  availableModels: vi.fn(),
  deleteProvider: vi.fn(),
  listProviders: vi.fn(),
  listSecretBackends: vi.fn(),
  listSecretRefs: vi.fn(),
  patchProvider: vi.fn(),
  secretStatus: vi.fn(),
  setSecret: vi.fn(),
  subscriptionAuthCallback: vi.fn(),
  subscriptionAuthCancel: vi.fn(),
  subscriptionAuthStart: vi.fn(),
  subscriptionAuthStatus: vi.fn(),
  testProvider: vi.fn(),
}))

import {
  availableModels,
  deleteProvider,
  listProviders,
  listSecretBackends,
  listSecretRefs,
  patchProvider,
  secretStatus,
  setSecret,
  subscriptionAuthStart,
  subscriptionAuthStatus,
  testProvider,
} from '../../api/client'
import { toast } from 'sonner'

const bedrockProvider: AdminProvider = {
  id: 'p1',
  name: 'AWS Bedrock',
  kind: 'api',
  driver: 'bedrock',
  base_url: 'us-east-1',
  default_model: '',
  credential_ref: '',
  headers: {},
  enabled: true,
}

const bedrockProviderWithRef: AdminProvider = {
  ...bedrockProvider,
  credential_ref: 'BEDROCK_KEY',
}

const cliProvider: AdminProvider = {
  id: 'p3',
  name: 'Claude Code',
  kind: 'cli',
  driver: 'claude-cli',
  base_url: '',
  default_model: 'sonnet',
  credential_ref: 'subscription',
  headers: {},
  enabled: true,
}

const cursorProvider: AdminProvider = {
  id: 'p4',
  name: 'Cursor',
  kind: 'cli',
  driver: 'cursor-cli',
  base_url: '',
  default_model: '',
  credential_ref: 'cursor-subscription',
  headers: {},
  enabled: true,
}

const openaicompatProvider: AdminProvider = {
  id: 'p2',
  name: 'Ollama',
  kind: 'api',
  driver: 'openaicompat',
  base_url: 'http://ollama.local/v1',
  default_model: 'qwen3',
  credential_ref: '',
  headers: {},
  enabled: true,
}

const chatgptSubscriptionProvider: AdminProvider = {
  id: 'p-cgpt',
  name: 'ChatGPT Subscription',
  kind: 'api',
  driver: 'openaicompat',
  base_url: 'http://cliproxy:8317/v1',
  default_model: 'gpt-4o',
  credential_ref: '',
  headers: {},
  enabled: true,
}

const claudeSubscriptionProvider: AdminProvider = {
  id: 'p-claude-sub',
  name: 'Claude Subscription',
  kind: 'api',
  driver: 'openaicompat',
  base_url: 'http://cliproxy:8317/v1',
  default_model: 'claude-3-7-sonnet-20250219',
  credential_ref: '',
  headers: {},
  enabled: true,
}

// formSaveButton finds the configuration form's own Save button
// (type="submit"), distinct from the independent credential panel's
// Save button that renders alongside it for non-cli providers.
function formSaveButton(): HTMLElement {
  return screen.getAllByRole('button', { name: 'Save' }).find((b) => b.getAttribute('type') === 'submit')!
}

function renderPage(id = 'p1') {
  return render(
    <MemoryRouter initialEntries={[`/settings/providers/${id}`]}>
      <Routes>
        <Route path="/settings/providers/:id" element={<ProviderEdit />} />
      </Routes>
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  // jsdom lacks scrollIntoView; Radix Select calls it on open.
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listProviders).mockResolvedValue([bedrockProvider])
  vi.mocked(availableModels).mockResolvedValue([])
  vi.mocked(secretStatus).mockResolvedValue({ configured: false, backend: '' })
  vi.mocked(listSecretBackends).mockResolvedValue([
    { backend: 'db', configured: true, default: true },
  ])
  vi.mocked(listSecretRefs).mockResolvedValue([])
})

describe('buildPatch', () => {
  it('carries name, credential_ref and default_model, and omits empty option keys', () => {
    const patch = buildPatch(openaicompatProvider, {
      name: 'Ollama',
      credential_ref: '',
      reasoningDisabled: false,
      request_timeout: '',
      region: 'us-east-1',
      auth: 'static',
      litellm_provider: '',
      default_model: 'qwen3',
    })
    expect(patch).toEqual({
      name: 'Ollama',
      credential_ref: '',
      default_model: 'qwen3',
      options: {},
    })
  })

  it('writes reasoning_effort = "none" only when staged on', () => {
    const patch = buildPatch(openaicompatProvider, {
      name: 'Ollama',
      credential_ref: '',
      reasoningDisabled: true,
      request_timeout: '',
      region: 'us-east-1',
      auth: 'static',
      litellm_provider: '',
      default_model: 'qwen3',
    })
    expect(patch.options).toEqual({ reasoning_effort: 'none' })
  })

  it('writes request_timeout only when non-empty', () => {
    const patch = buildPatch(openaicompatProvider, {
      name: 'Ollama',
      credential_ref: '',
      reasoningDisabled: false,
      request_timeout: '20m',
      region: 'us-east-1',
      auth: 'static',
      litellm_provider: '',
      default_model: 'qwen3',
    })
    expect(patch.options).toEqual({ request_timeout: '20m' })
  })

  it('always carries region for a bedrock provider', () => {
    const patch = buildPatch(bedrockProvider, {
      name: 'AWS Bedrock',
      credential_ref: '',
      reasoningDisabled: false,
      request_timeout: '',
      region: 'eu-west-1',
      auth: 'static',
      litellm_provider: '',
      default_model: '',
    })
    expect(patch.options).toEqual({ region: 'eu-west-1' })
  })

  it('cloud identity sets options.auth and clears the credential reference', () => {
    const patch = buildPatch(bedrockProviderWithRef, {
      name: 'AWS Bedrock',
      credential_ref: 'BEDROCK_KEY',
      reasoningDisabled: false,
      request_timeout: '',
      region: 'eu-west-1',
      auth: 'ambient',
      litellm_provider: '',
      default_model: '',
    })
    expect(patch.credential_ref).toBe('')
    expect(patch.options).toEqual({ region: 'eu-west-1', auth: 'ambient' })
  })

  it('switching an ambient row back to access keys drops options.auth', () => {
    const patch = buildPatch(
      { ...bedrockProvider, options: { region: 'eu-west-1', auth: 'ambient' } },
      {
        name: 'AWS Bedrock',
        credential_ref: 'BEDROCK_KEY',
        reasoningDisabled: false,
        request_timeout: '',
        region: 'eu-west-1',
        auth: 'static',
        litellm_provider: '',
        default_model: '',
      },
    )
    expect(patch.credential_ref).toBe('BEDROCK_KEY')
    expect(patch.options).toEqual({ region: 'eu-west-1' })
  })

  it('omits region for a non-bedrock provider', () => {
    const patch = buildPatch(openaicompatProvider, {
      name: 'Ollama',
      credential_ref: '',
      reasoningDisabled: false,
      request_timeout: '',
      region: 'eu-west-1',
      auth: 'static',
      litellm_provider: '',
      default_model: 'qwen3',
    })
    expect(patch.options).toEqual({})
  })

  it('writes litellm_provider only when non-empty', () => {
    const patch = buildPatch(openaicompatProvider, {
      name: 'Ollama',
      credential_ref: '',
      reasoningDisabled: false,
      request_timeout: '',
      region: 'us-east-1',
      auth: 'static',
      litellm_provider: 'xai',
      default_model: 'qwen3',
    })
    expect(patch.options).toEqual({ litellm_provider: 'xai' })
  })

  it('keeps an unrelated options key this form never shows', () => {
    const patch = buildPatch(
      { ...openaicompatProvider, options: { anthropic_base_url: 'https://example.test' } },
      {
        name: 'Ollama',
        credential_ref: '',
        reasoningDisabled: false,
        request_timeout: '',
        region: 'us-east-1',
        auth: 'static',
        litellm_provider: '',
        default_model: 'qwen3',
      },
    )
    expect(patch.options).toEqual({ anthropic_base_url: 'https://example.test' })
  })

  it('clears request_timeout without touching an unrelated key', () => {
    const patch = buildPatch(
      { ...openaicompatProvider, options: { request_timeout: '20m', anthropic_base_url: 'https://example.test' } },
      {
        name: 'Ollama',
        credential_ref: '',
        reasoningDisabled: false,
        request_timeout: '',
        region: 'us-east-1',
        auth: 'static',
        litellm_provider: '',
        default_model: 'qwen3',
      },
    )
    expect(patch.options).toEqual({ anthropic_base_url: 'https://example.test' })
    expect(patch.options).not.toHaveProperty('request_timeout')
  })

  it('turning reasoning off removes reasoning_effort while keeping other keys', () => {
    const patch = buildPatch(
      { ...openaicompatProvider, options: { reasoning_effort: 'none', anthropic_base_url: 'https://example.test' } },
      {
        name: 'Ollama',
        credential_ref: '',
        reasoningDisabled: false,
        request_timeout: '',
        region: 'us-east-1',
        auth: 'static',
        litellm_provider: '',
        default_model: 'qwen3',
      },
    )
    expect(patch.options).toEqual({ anthropic_base_url: 'https://example.test' })
    expect(patch.options).not.toHaveProperty('reasoning_effort')
  })
})

describe('ProviderEdit staged form commit semantics', () => {
  it('sends zero PATCH before Save', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    renderPage('p2')

    await screen.findByDisplayValue('Ollama')
    fireEvent.click(await screen.findByRole('switch', { name: 'Disable reasoning' }))
    fireEvent.change(screen.getByPlaceholderText('5m'), { target: { value: '20m' } })

    expect(patchProvider).not.toHaveBeenCalled()
  })

  it('sends exactly one PATCH on Save containing every staged field', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p2')

    await screen.findByDisplayValue('Ollama')
    fireEvent.click(screen.getByRole('switch', { name: 'Disable reasoning' }))
    fireEvent.change(screen.getByPlaceholderText('5m'), { target: { value: '20m' } })

    fireEvent.click(formSaveButton())

    await waitFor(() => expect(patchProvider).toHaveBeenCalledTimes(1))
    expect(patchProvider).toHaveBeenCalledWith('p2', {
      name: 'Ollama',
      credential_ref: '',
      default_model: 'qwen3',
      options: { reasoning_effort: 'none', request_timeout: '20m' },
    })
  })

  it('shows the Unsaved changes note only while dirty, and disables Save until dirty', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    renderPage('p2')

    await screen.findByDisplayValue('Ollama')
    expect(screen.queryByText('Unsaved changes')).not.toBeInTheDocument()
    expect(formSaveButton()).toBeDisabled()

    fireEvent.change(screen.getByPlaceholderText('5m'), { target: { value: '20m' } })

    expect(screen.getByText('Unsaved changes')).toBeInTheDocument()
    expect(formSaveButton()).not.toBeDisabled()
  })

  it('Cancel restores the loaded values and clears dirty', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    renderPage('p2')

    const input = (await screen.findByPlaceholderText('5m')) as HTMLInputElement
    fireEvent.change(input, { target: { value: '20m' } })
    expect(input.value).toBe('20m')

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(input.value).toBe('')
    expect(screen.queryByText('Unsaved changes')).not.toBeInTheDocument()
    expect(patchProvider).not.toHaveBeenCalled()
  })

  it('a failed Save keeps the staged values and shows a retryable alert', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(patchProvider).mockRejectedValueOnce(new Error('server exploded')).mockResolvedValueOnce()
    renderPage('p2')

    const input = (await screen.findByPlaceholderText('5m')) as HTMLInputElement
    fireEvent.change(input, { target: { value: '20m' } })
    fireEvent.click(formSaveButton())

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('server exploded')
    expect(input.value).toBe('20m')

    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(patchProvider).toHaveBeenCalledTimes(2))
  })

  it('keeps the persisted name as the header title until Save succeeds', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    renderPage('p2')

    const nameInput = await screen.findByLabelText('Provider name')
    fireEvent.change(nameInput, { target: { value: 'renamed' } })

    expect(screen.getByRole('heading', { name: 'Ollama' })).toBeInTheDocument()
  })

  it('name is the first field in the configuration form', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    renderPage('p2')

    const nameInput = await screen.findByLabelText('Provider name')
    expect(nameInput).toBeInTheDocument()
  })

  it('rebase keeps a touched field after a Test refetch', async () => {
    vi.mocked(listProviders)
      .mockResolvedValueOnce([openaicompatProvider])
      .mockResolvedValueOnce([{ ...openaicompatProvider, options: { request_timeout: '30m' } }])
    vi.mocked(testProvider).mockResolvedValue({ ok: true, latency_ms: 10, model: 'qwen3' })
    renderPage('p2')

    const input = (await screen.findByPlaceholderText('5m')) as HTMLInputElement
    fireEvent.change(input, { target: { value: '99m' } })

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await screen.findByText(/^OK,/)

    expect(input.value).toBe('99m')
  })

  it('a failed test shows the raw detail behind a Details disclosure', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(testProvider).mockResolvedValue({
      ok: false,
      latency_ms: 5,
      model: 'qwen3',
      detail: 'connection refused',
    })
    renderPage('p2')

    fireEvent.click(await screen.findByRole('button', { name: 'Test connection' }))
    await screen.findByText(/Failed after 5 ms/)

    expect(screen.queryByText('connection refused')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Details' }))
    expect(screen.getByText('connection refused')).toBeInTheDocument()
  })

  it('stays on the not-tested-yet state when the probe reports a Timothy auth failure', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(testProvider).mockResolvedValue({
      ok: false,
      latency_ms: 0,
      model: 'qwen3',
      detail: 'missing or invalid bearer token',
    })
    renderPage('p2')

    fireEvent.click(await screen.findByRole('button', { name: 'Test connection' }))

    await waitFor(() => expect(testProvider).toHaveBeenCalled())
    expect(await screen.findByText('Not tested yet.')).toBeInTheDocument()
  })

  it('stays on the not-tested-yet state when testProvider throws a Timothy auth error', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(testProvider).mockRejectedValue({ status: 401, message: 'missing or invalid bearer token' })
    renderPage('p2')

    fireEvent.click(await screen.findByRole('button', { name: 'Test connection' }))

    await waitFor(() => expect(testProvider).toHaveBeenCalled())
    expect(await screen.findByText('Not tested yet.')).toBeInTheDocument()
  })

  it('renders a failed status when testProvider throws a plain error', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(testProvider).mockRejectedValue(new Error('socket hang up'))
    renderPage('p2')

    fireEvent.click(await screen.findByRole('button', { name: 'Test connection' }))

    expect(await screen.findByText(/socket hang up/)).toBeInTheDocument()
  })

  it('re-runs the test from the failed status action button', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(testProvider)
      .mockResolvedValueOnce({ ok: false, latency_ms: 5, model: 'qwen3', detail: 'connection refused' })
      .mockResolvedValueOnce({ ok: true, latency_ms: 8, model: 'qwen3' })
    renderPage('p2')

    fireEvent.click(await screen.findByRole('button', { name: 'Test connection' }))
    await screen.findByText(/Failed after 5 ms/)

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await screen.findByText(/^OK,/)
    expect(testProvider).toHaveBeenCalledTimes(2)
  })
})

describe('ProviderEdit load failure', () => {
  it('reports a toast when listProviders rejects, and renders nothing', async () => {
    vi.mocked(listProviders).mockRejectedValue(new Error('network down'))
    const { container } = renderPage('p1')

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not load provider', { description: 'network down' }),
    )
    expect(container).toBeEmptyDOMElement()
  })
})

describe('ProviderEdit delete flow', () => {
  it('opens the confirm dialog on Delete and removes the provider on confirm', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(deleteProvider).mockResolvedValue()
    renderPage('p2')

    await screen.findByDisplayValue('Ollama')
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(await screen.findByText('Delete Ollama?')).toBeInTheDocument()

    const confirmButtons = await screen.findAllByRole('button', { name: 'Delete' })
    fireEvent.click(confirmButtons[confirmButtons.length - 1])
    await waitFor(() => expect(deleteProvider).toHaveBeenCalledWith('p2'))
  })

  it('closes the confirm dialog and shows a toast when delete fails', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(deleteProvider).mockRejectedValue(new Error('in use by a route'))
    renderPage('p2')

    await screen.findByDisplayValue('Ollama')
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not remove provider', { description: 'in use by a route' }),
    )
    expect(screen.queryByText('Delete Ollama?')).not.toBeInTheDocument()
  })
})

describe('ProviderEdit credential_ref and litellm_provider fields', () => {
  it('staging credential_ref then Save sends the new value', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(listSecretRefs).mockResolvedValue([
      { name: 'OTHER_KEY', backend: 'db', referenced_by: [] },
    ])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p2')

    await screen.findByDisplayValue('Ollama')
    fireEvent.click(screen.getByLabelText('existing credential'))
    fireEvent.click(await screen.findByRole('option', { name: /OTHER_KEY/ }))

    fireEvent.click(formSaveButton())
    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p2', expect.objectContaining({ credential_ref: 'OTHER_KEY' })),
    )
  })

  it('staging the LiteLLM provider field then Save sends it in options', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p2')

    const input = await screen.findByPlaceholderText('e.g. xai, zai')
    fireEvent.change(input, { target: { value: 'zai' } })
    fireEvent.click(formSaveButton())

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p2', expect.objectContaining({ options: expect.objectContaining({ litellm_provider: 'zai' }) })),
    )
  })
})

describe('ProviderEdit credential panel', () => {
  const withRef = { ...openaicompatProvider, credential_ref: 'OLLAMA_API_KEY' }

  it('offers no clear control for a configured key', async () => {
    vi.mocked(listProviders).mockResolvedValue([withRef])
    vi.mocked(secretStatus).mockResolvedValue({ configured: true, backend: 'db' })
    renderPage('p2')

    expect(await screen.findByPlaceholderText('paste new key to rotate')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /clear/i })).toBeNull()
  })

  it('shows the key panel for a kind=cli provider and writes under its credential_ref', async () => {
    vi.mocked(listProviders).mockResolvedValue([cursorProvider])
    vi.mocked(secretStatus).mockResolvedValue({ configured: false, backend: '' })
    vi.mocked(setSecret).mockResolvedValue()
    renderPage('p4')

    expect(await screen.findByText('API key')).toBeInTheDocument()
    expect(await screen.findByText('not set')).toBeInTheDocument()
    const input = screen.getByPlaceholderText('paste key')
    fireEvent.change(input, { target: { value: 'cursor-key' } })
    const saveButtons = screen.getAllByRole('button', { name: 'Save' })
    fireEvent.click(saveButtons[saveButtons.length - 1])

    await waitFor(() => expect(setSecret).toHaveBeenCalledWith('cursor-subscription', 'cursor-key'))
  })

  it('titles the panel Subscription token for a claude-cli provider', async () => {
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    renderPage('p3')

    expect(await screen.findByText('Subscription token')).toBeInTheDocument()
  })

  it('renders subscription authentication panel for cliproxy chatgpt and claude providers', async () => {
    vi.mocked(listProviders).mockResolvedValue([chatgptSubscriptionProvider])
    renderPage('p-cgpt')

    expect(await screen.findByText('Subscription authentication')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Re-authenticate with OpenAI' })).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('paste key')).toBeNull()
  })

  it('renders claude reauth button for cliproxy claude provider', async () => {
    vi.mocked(listProviders).mockResolvedValue([claudeSubscriptionProvider])
    renderPage('p-claude-sub')

    expect(await screen.findByText('Subscription authentication')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Re-authenticate with Claude' })).toBeInTheDocument()
  })

  it('starts re-authentication and handles success status for cliproxy chatgpt', async () => {
    vi.mocked(listProviders).mockResolvedValue([chatgptSubscriptionProvider])
    vi.mocked(subscriptionAuthStart).mockResolvedValue({ status: 'ok', url: 'https://auth.openai.com/oauth', state: 'test-state' })
    vi.mocked(subscriptionAuthStatus).mockResolvedValue({ status: 'ok' })
    const windowOpenSpy = vi.spyOn(window, 'open').mockImplementation(() => null)

    renderPage('p-cgpt')

    const button = await screen.findByRole('button', { name: 'Re-authenticate with OpenAI' })
    fireEvent.click(button)

    await waitFor(() => expect(subscriptionAuthStart).toHaveBeenCalledWith('chatgpt'))
    expect(windowOpenSpy).toHaveBeenCalledWith('https://auth.openai.com/oauth', '_blank', 'noopener,noreferrer')
    await waitFor(() => expect(subscriptionAuthStatus).toHaveBeenCalledWith('test-state'))
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Authentication renewed', expect.anything()))

    windowOpenSpy.mockRestore()
  })

  it('omits credential reference, reasoning, request timeout, and litellm provider for cliproxy providers', async () => {
    vi.mocked(listProviders).mockResolvedValue([chatgptSubscriptionProvider])
    renderPage('p-cgpt')

    await screen.findByDisplayValue('ChatGPT Subscription')
    expect(screen.queryByText('Credential reference')).toBeNull()
    expect(screen.queryByRole('switch', { name: 'Disable reasoning' })).toBeNull()
    expect(screen.queryByLabelText('Request timeout')).toBeNull()
    expect(screen.queryByLabelText('LiteLLM provider')).toBeNull()
  })

  it('omits litellm provider for anthropic direct API provider', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      {
        id: 'p-anthropic',
        name: 'Anthropic',
        kind: 'api',
        driver: 'anthropic',
        base_url: '',
        default_model: 'claude-haiku-4-5',
        credential_ref: 'ANTHROPIC_API_KEY',
        headers: {},
        enabled: true,
      },
    ])
    renderPage('p-anthropic')

    await screen.findByDisplayValue('Anthropic')
    expect(screen.queryByLabelText('LiteLLM provider')).toBeNull()
  })

  it('typing a key and Save rotates the stored secret for a non-bedrock provider', async () => {
    vi.mocked(listProviders).mockResolvedValue([withRef])
    vi.mocked(setSecret).mockResolvedValue()
    renderPage('p2')

    const input = await screen.findByPlaceholderText('paste key')
    fireEvent.change(input, { target: { value: 'new-key-value' } })
    const saveButtons = screen.getAllByRole('button', { name: 'Save' })
    fireEvent.click(saveButtons[saveButtons.length - 1])

    await waitFor(() => expect(setSecret).toHaveBeenCalledWith('OLLAMA_API_KEY', 'new-key-value'))
  })
})

describe('ProviderEdit default model field', () => {
  it('shows the current default model', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      { ...bedrockProvider, default_model: 'us.amazon.nova-pro-v1:0' },
    ])
    renderPage()

    expect(await screen.findByPlaceholderText('model id')).toHaveValue('us.amazon.nova-pro-v1:0')
  })

  it('staging a typed model id then Save sends one PATCH with options.default_model', async () => {
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage()

    const input = await screen.findByPlaceholderText('model id')
    fireEvent.change(input, { target: { value: 'us.amazon.nova-pro-v1:0' } })
    expect(patchProvider).not.toHaveBeenCalled()

    fireEvent.click(formSaveButton())
    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', expect.objectContaining({ default_model: 'us.amazon.nova-pro-v1:0' })),
    )
  })

  it('picking a suggestion from the unified list stages it, and Save sends the PATCH', async () => {
    vi.mocked(availableModels).mockResolvedValue([
      { id: 'amazon.nova-lite-v1:0', source: 'catalog', input_per_mtok: 0.06, output_per_mtok: 0.24 },
      { id: 'amazon.nova-pro-v1:0', source: 'catalog' },
    ])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage()

    const input = await screen.findByPlaceholderText('model id')
    await waitFor(() => expect(availableModels).toHaveBeenCalledWith('p1'))
    fireEvent.focus(input)
    fireEvent.change(input, { target: { value: 'nova-lite' } })

    fireEvent.click(await screen.findByRole('option', { name: /amazon\.nova-lite-v1:0/ }))
    expect(screen.queryByRole('option', { name: /amazon\.nova-pro-v1:0/ })).not.toBeInTheDocument()
    expect(patchProvider).not.toHaveBeenCalled()

    fireEvent.click(formSaveButton())
    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', expect.objectContaining({ default_model: 'amazon.nova-lite-v1:0' })),
    )
  })

  it('filters suggestions client-side by substring', async () => {
    vi.mocked(availableModels).mockResolvedValue([
      { id: 'qwen3', source: 'live' },
      { id: 'llama3.3', source: 'live' },
    ])
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    renderPage('p2')

    const input = await screen.findByPlaceholderText('model id')
    await waitFor(() => expect(availableModels).toHaveBeenCalledTimes(1))
    fireEvent.focus(input)
    fireEvent.change(input, { target: { value: 'llama' } })

    expect(await screen.findByRole('option', { name: /llama3\.3/ })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: /qwen3/ })).not.toBeInTheDocument()
    expect(availableModels).toHaveBeenCalledTimes(1)
  })

  it('offers the default model as the one suggestion and the typed-id hint when the list is empty', async () => {
    vi.mocked(availableModels).mockResolvedValue([])
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    renderPage('p2')

    const input = await screen.findByPlaceholderText('model id')
    expect(await screen.findByText(/Used when a route chain entry for this provider names no model\. This provider lists no models/)).toBeInTheDocument()
    fireEvent.change(input, { target: { value: '' } })
    fireEvent.focus(input)

    const options = await screen.findAllByRole('option')
    expect(options).toHaveLength(1)
    expect(options[0]).toHaveTextContent('qwen3')
  })

  it('omits the section for a kind=cli provider (it gets its own alias picker instead)', async () => {
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    renderPage('p3')

    await screen.findByDisplayValue('Claude Code')
    expect(screen.getAllByPlaceholderText('sonnet')).toHaveLength(1)
    expect(screen.queryByPlaceholderText('model id')).not.toBeInTheDocument()
  })
})

describe('ProviderEdit models panel', () => {
  it('titles a live list "Models your key can use" with price and context columns', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(availableModels).mockResolvedValue([
      { id: 'qwen3', source: 'live', input_per_mtok: 0.06, output_per_mtok: 0.24, max_input_tokens: 300000 },
      { id: 'house-model', source: 'live', input_per_mtok: null, output_per_mtok: null, max_input_tokens: null },
    ])
    renderPage('p2')

    expect(await screen.findByText('Models your key can use · 2')).toBeInTheDocument()
    expect(screen.getByText('in $0.06 · out $0.24 /MTok')).toBeInTheDocument()
    expect(screen.getByText('300k ctx')).toBeInTheDocument()
    expect(screen.getByText('house-model')).toBeInTheDocument()
    expect(screen.getByText('unpriced')).toBeInTheDocument()
    expect(screen.getAllByText('unknown')).toHaveLength(2)
    expect(screen.queryByText('operator')).not.toBeInTheDocument()

    expect(screen.queryAllByRole('checkbox')).toHaveLength(0)
    expect(screen.queryByRole('button', { name: 'Add' })).not.toBeInTheDocument()
  })

  it('titles a catalog fallback "Models from catalog"', async () => {
    vi.mocked(availableModels).mockResolvedValue([{ id: 'amazon.nova-lite-v1:0', source: 'catalog' }])
    renderPage()

    expect(await screen.findByText('Models from catalog · 1')).toBeInTheDocument()
    expect(screen.queryByText(/Models your key can use/)).not.toBeInTheDocument()
  })

  it('shows the typed-id hint when the list is empty', async () => {
    vi.mocked(availableModels).mockResolvedValue([])
    renderPage()

    expect(await screen.findByText('no models found')).toBeInTheDocument()
    expect(screen.getAllByText('This provider lists no models. Type any model id it accepts.').length).toBeGreaterThan(0)
  })

  it('shows the listing error instead of an empty message', async () => {
    vi.mocked(availableModels).mockRejectedValue(new Error('http 401: Incorrect API key provided'))
    renderPage()

    expect(await screen.findByText(/Could not list models: .*Incorrect API key provided/)).toBeInTheDocument()
  })

  it('filters the list client-side without another fetch', async () => {
    vi.mocked(availableModels).mockResolvedValue([
      { id: 'amazon.nova-lite-v1:0', source: 'catalog' },
      { id: 'anthropic.claude-x', source: 'catalog' },
    ])
    renderPage()

    const filter = await screen.findByPlaceholderText('filter by model id…')
    await screen.findByText('anthropic.claude-x')
    fireEvent.change(filter, { target: { value: 'nova' } })

    expect(screen.getByText('amazon.nova-lite-v1:0')).toBeInTheDocument()
    expect(screen.queryByText('anthropic.claude-x')).not.toBeInTheDocument()
    expect(availableModels).toHaveBeenCalledTimes(1)
  })
})

describe('ProviderEdit model prices', () => {
  const pricedProvider: AdminProvider = {
    ...openaicompatProvider,
    options: {
      request_timeout: '5m',
      prices_by_model: '{"qwen3":{"input_per_mtok":0.1,"output_per_mtok":0.2,"cache_read_per_mtok":0.01},"unlisted":{"input_per_mtok":1,"output_per_mtok":2}}',
    },
  }
  const listed = [
    { id: 'qwen3', source: 'live' as const, input_per_mtok: 0.1, output_per_mtok: 0.2, price_source: 'operator' as const },
    { id: 'llama3.3', source: 'live' as const, input_per_mtok: 0.3, output_per_mtok: 0.4, price_source: 'listing' as const },
    { id: 'glm-4.7', source: 'live' as const, input_per_mtok: 0.5, output_per_mtok: 0.6, price_source: 'catalog' as const },
    { id: 'house-model', source: 'live' as const },
  ]

  function row(id: string): HTMLElement {
    return screen.getByText(id, { selector: 'td' }).closest('tr')!
  }

  function lastPricesByModel(): unknown {
    const [, patch] = vi.mocked(patchProvider).mock.calls.at(-1)!
    return JSON.parse(patch.options!.prices_by_model!)
  }

  beforeEach(() => {
    vi.mocked(listProviders).mockResolvedValue([pricedProvider])
    vi.mocked(availableModels).mockResolvedValue(listed)
    vi.mocked(patchProvider).mockResolvedValue()
  })

  it('labels each price with its source, unknown when none', async () => {
    renderPage('p2')
    await screen.findByText('house-model')

    expect(row('qwen3')).toHaveTextContent('operator')
    expect(row('llama3.3')).toHaveTextContent('listing')
    expect(row('glm-4.7')).toHaveTextContent('catalog')
    expect(row('house-model')).toHaveTextContent('unpriced unknown')
    expect(row('unlisted')).toHaveTextContent('in $1 · out $2 /MTok operator')
  })

  it('saves an edited price into prices_by_model, keeping other options and entry keys', async () => {
    renderPage('p2')
    await screen.findByText('house-model')

    fireEvent.click(screen.getByRole('button', { name: 'Edit price for qwen3' }))
    expect(screen.getByLabelText('Input $ / MTok')).toHaveValue('0.1')
    fireEvent.change(screen.getByLabelText('Input $ / MTok'), { target: { value: '0.5' } })
    fireEvent.change(screen.getByLabelText('Output $ / MTok'), { target: { value: '1.5' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save price' }))

    await waitFor(() => expect(patchProvider).toHaveBeenCalledTimes(1))
    const [id, patch] = vi.mocked(patchProvider).mock.calls[0]
    expect(id).toBe('p2')
    expect(Object.keys(patch)).toEqual(['options'])
    expect(patch.options?.request_timeout).toBe('5m')
    expect(lastPricesByModel()).toEqual({
      qwen3: { input_per_mtok: 0.5, output_per_mtok: 1.5, cache_read_per_mtok: 0.01 },
      unlisted: { input_per_mtok: 1, output_per_mtok: 2 },
    })
  })

  it('refetches the model list when the saved prices change', async () => {
    renderPage('p2')
    await screen.findByText('house-model')
    vi.mocked(listProviders).mockResolvedValue([
      { ...pricedProvider, options: { ...pricedProvider.options, prices_by_model: '{"house-model":{"input_per_mtok":1,"output_per_mtok":1}}' } },
    ])

    fireEvent.click(screen.getByRole('button', { name: 'Edit price for house-model' }))
    fireEvent.change(screen.getByLabelText('Input $ / MTok'), { target: { value: '1' } })
    fireEvent.change(screen.getByLabelText('Output $ / MTok'), { target: { value: '1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save price' }))

    await waitFor(() => expect(availableModels).toHaveBeenCalledTimes(2))
    expect(screen.queryByRole('button', { name: 'Save price' })).not.toBeInTheDocument()
  })

  it('shows validation errors inline and does not save', async () => {
    renderPage('p2')
    await screen.findByText('house-model')

    fireEvent.click(screen.getByRole('button', { name: 'Edit price for house-model' }))
    fireEvent.change(screen.getByLabelText('Input $ / MTok'), { target: { value: '-1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save price' }))

    expect(await screen.findByText('Cannot be negative.')).toBeInTheDocument()
    expect(screen.getByText('Enter a price.')).toBeInTheDocument()
    expect(patchProvider).not.toHaveBeenCalled()
  })

  it('shows a rejected save inline', async () => {
    vi.mocked(patchProvider).mockRejectedValue(new Error('options.prices_by_model "x": input_per_mtok is negative (-1)'))
    renderPage('p2')
    await screen.findByText('house-model')

    fireEvent.click(screen.getByRole('button', { name: 'Edit price for house-model' }))
    fireEvent.change(screen.getByLabelText('Input $ / MTok'), { target: { value: '1' } })
    fireEvent.change(screen.getByLabelText('Output $ / MTok'), { target: { value: '2' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save price' }))

    expect(await screen.findByText(/Could not save price: .*is negative/)).toBeInTheDocument()
  })

  it('clears an operator price, dropping only that model', async () => {
    renderPage('p2')
    await screen.findByText('house-model')

    fireEvent.click(screen.getByRole('button', { name: 'Edit price for house-model' }))
    expect(screen.queryByRole('button', { name: 'Clear price' })).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Edit price for qwen3' }))
    fireEvent.click(screen.getByRole('button', { name: 'Clear price' }))

    await waitFor(() => expect(patchProvider).toHaveBeenCalledTimes(1))
    expect(lastPricesByModel()).toEqual({ unlisted: { input_per_mtok: 1, output_per_mtok: 2 } })
  })

  it('deletes prices_by_model when the last operator price is cleared', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      { ...openaicompatProvider, options: { prices_by_model: '{"qwen3":{"input_per_mtok":0.1,"output_per_mtok":0.2}}' } },
    ])
    renderPage('p2')
    await screen.findByText('house-model')

    fireEvent.click(screen.getByRole('button', { name: 'Edit price for qwen3' }))
    fireEvent.click(screen.getByRole('button', { name: 'Clear price' }))

    await waitFor(() => expect(patchProvider).toHaveBeenCalledWith('p2', { options: {} }))
  })

  it('adds a price for a typed model id that is not listed', async () => {
    renderPage('p2')
    await screen.findByText('house-model')

    fireEvent.click(screen.getByRole('button', { name: 'Add price' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save price' }))
    expect(await screen.findByText('Enter a model id.')).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText(/^Model id/), { target: { value: ' private-model ' } })
    fireEvent.change(screen.getByLabelText(/^Input \$ \/ MTok/), { target: { value: '2' } })
    fireEvent.change(screen.getByLabelText(/^Output \$ \/ MTok/), { target: { value: '8' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save price' }))

    await waitFor(() => expect(patchProvider).toHaveBeenCalledTimes(1))
    expect(lastPricesByModel()).toMatchObject({ 'private-model': { input_per_mtok: 2, output_per_mtok: 8 } })
  })

  it('marks every listed model without an operator price free and says so', async () => {
    renderPage('p2')
    await screen.findByText('house-model')

    fireEvent.click(screen.getByRole('button', { name: 'Mark listed models free' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Mark free' }))

    await waitFor(() => expect(patchProvider).toHaveBeenCalledTimes(1))
    expect(lastPricesByModel()).toEqual({
      qwen3: { input_per_mtok: 0.1, output_per_mtok: 0.2, cache_read_per_mtok: 0.01 },
      unlisted: { input_per_mtok: 1, output_per_mtok: 2 },
      'llama3.3': { input_per_mtok: 0, output_per_mtok: 0 },
      'glm-4.7': { input_per_mtok: 0, output_per_mtok: 0 },
      'house-model': { input_per_mtok: 0, output_per_mtok: 0 },
    })
    expect(await screen.findByText('Marked 3 listed models free.')).toBeInTheDocument()
  })

  it('disables "Mark listed models free" when every listed model has an operator price', async () => {
    vi.mocked(availableModels).mockResolvedValue([listed[0]])
    renderPage('p2')
    await screen.findByText('unlisted')

    expect(screen.getByRole('button', { name: 'Mark listed models free' })).toBeDisabled()
  })
})

describe('ProviderEdit cli (subscription) provider', () => {
  it('shows the current default model in the picker, not an editable declared-models list', async () => {
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    renderPage('p3')

    await screen.findByDisplayValue('Claude Code')
    expect(screen.getByPlaceholderText('sonnet')).toHaveValue('sonnet')
    expect(screen.queryByPlaceholderText('model id')).not.toBeInTheDocument()
  })

  it('offers the CLI aliases as suggestions in the default model picker', async () => {
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    renderPage('p3')

    const input = await screen.findByPlaceholderText('sonnet')
    fireEvent.change(input, { target: { value: '' } })
    fireEvent.focus(input)

    expect(await screen.findByRole('option', { name: /^fable/ })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: /^sonnet/ })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: /^opus/ })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: /^haiku/ })).toBeInTheDocument()
  })

  it('adds a listed model not already among the CLI aliases to the suggestion list', async () => {
    vi.mocked(availableModels).mockResolvedValue([{ id: 'claude-opus-4-6-20261015', source: 'catalog' }])
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    renderPage('p3')

    const input = await screen.findByPlaceholderText('sonnet')
    await waitFor(() => expect(availableModels).toHaveBeenCalledWith('p3'))
    fireEvent.change(input, { target: { value: '' } })
    fireEvent.focus(input)

    expect(await screen.findByRole('option', { name: /claude-opus-4-6-20261015/ })).toBeInTheDocument()
  })

  it('hides the Test connection button since there is no chat driver to probe', async () => {
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    renderPage('p3')

    await screen.findByDisplayValue('Claude Code')
    expect(screen.queryByRole('button', { name: 'Test connection' })).not.toBeInTheDocument()
  })
})

describe('ProviderEdit cursor-cli provider', () => {
  it('offers cursor\'s live model list as suggestions', async () => {
    vi.mocked(listProviders).mockResolvedValue([cursorProvider])
    vi.mocked(availableModels).mockResolvedValue([
      { id: 'composer-2.5', display_name: 'Composer 2.5' },
      { id: 'sonic' },
    ])
    renderPage('p4')

    const input = await screen.findByPlaceholderText('composer-2.5')
    await waitFor(() => expect(availableModels).toHaveBeenCalledWith('p4'))
    fireEvent.focus(input)

    expect(await screen.findByRole('option', { name: /Composer 2\.5/ })).toBeInTheDocument()
    expect(screen.getByRole('option', { name: /^sonic/ })).toBeInTheDocument()
  })

  it('falls back to free text with no error toast when the fetch fails, and stages it', async () => {
    vi.mocked(listProviders).mockResolvedValue([cursorProvider])
    vi.mocked(availableModels).mockRejectedValue(new Error('502'))
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p4')

    const input = await screen.findByPlaceholderText('composer-2.5')
    fireEvent.focus(input)
    fireEvent.change(input, { target: { value: 'anything' } })

    fireEvent.click(formSaveButton())
    await waitFor(() => expect(patchProvider).toHaveBeenCalledWith('p4', expect.objectContaining({ default_model: 'anything' })))
    expect(screen.queryByText(/could not/i)).not.toBeInTheDocument()
  })
})

describe('ProviderEdit reasoning field', () => {
  it('omits the reasoning field for non-openaicompat drivers', async () => {
    vi.mocked(listProviders).mockResolvedValue([bedrockProvider])
    renderPage('p1')

    await screen.findByDisplayValue('AWS Bedrock')
    expect(screen.queryByRole('switch', { name: 'Disable reasoning' })).toBeNull()
  })

  it('reflects the staged value immediately with no pending spinner', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    renderPage('p2')

    const toggle = await screen.findByRole('switch', { name: 'Disable reasoning' })
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    fireEvent.click(toggle)
    expect(toggle.getAttribute('aria-checked')).toBe('true')
    expect(patchProvider).not.toHaveBeenCalled()
  })
})

describe('ProviderEdit authentication field', () => {
  it('omits the field for non-bedrock drivers', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    renderPage('p2')

    await screen.findByDisplayValue('Ollama')
    expect(screen.queryByRole('combobox', { name: 'Authentication' })).toBeNull()
  })

  it('shows cloud identity for an ambient row and hides the credential reference and key panel', async () => {
    vi.mocked(listProviders).mockResolvedValue([{ ...bedrockProvider, options: { region: 'eu-west-1', auth: 'ambient' } }])
    renderPage('p1')

    expect(await screen.findByRole('combobox', { name: 'Authentication' })).toHaveTextContent('Cloud identity')
    expect(screen.queryByText('Credential reference')).toBeNull()
    expect(screen.queryByText('AWS credentials')).toBeNull()
    expect(screen.queryByPlaceholderText('AKIA…')).toBeNull()
  })

  it('switching a static row to cloud identity hides the key panel and saves an ambient patch', async () => {
    vi.mocked(listProviders).mockResolvedValue([bedrockProviderWithRef])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p1')

    expect(await screen.findByRole('combobox', { name: 'Authentication' })).toHaveTextContent('Access keys')
    expect(screen.getByText('AWS credentials')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('combobox', { name: 'Authentication' }))
    fireEvent.click(await screen.findByRole('option', { name: 'Cloud identity' }))

    expect(screen.queryByText('AWS credentials')).toBeNull()
    expect(screen.queryByText('Credential reference')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(patchProvider).toHaveBeenCalled())
    expect(vi.mocked(patchProvider).mock.calls[0][1]).toMatchObject({
      credential_ref: '',
      options: { region: 'us-east-1', auth: 'ambient' },
    })
  })
})

describe('ProviderEdit region field', () => {
  it('omits the region field for non-bedrock drivers', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    renderPage('p2')

    await screen.findByDisplayValue('Ollama')
    expect(screen.queryByText('AWS region')).toBeNull()
  })

  it('defaults the region dropdown to us-east-1 when options.region is unset', async () => {
    vi.mocked(listProviders).mockResolvedValue([bedrockProvider])
    renderPage('p1')

    expect(await screen.findByRole('combobox', { name: 'AWS region' })).toHaveTextContent('us-east-1 (N. Virginia)')
  })

  it('shows the stored region when options.region is set', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      { ...bedrockProvider, options: { region: 'eu-west-1' } },
    ])
    renderPage('p1')

    expect(await screen.findByRole('combobox', { name: 'AWS region' })).toHaveTextContent('eu-west-1 (Ireland)')
  })

  it('picking a new region stages it, and Save sends the PATCH', async () => {
    vi.mocked(listProviders).mockResolvedValue([bedrockProvider])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p1')

    fireEvent.click(await screen.findByRole('combobox', { name: 'AWS region' }))
    fireEvent.click(await screen.findByRole('option', { name: 'ap-southeast-2 (Sydney)' }))
    expect(patchProvider).not.toHaveBeenCalled()

    fireEvent.click(screen.getAllByRole('button', { name: 'Save' })[0])
    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', expect.objectContaining({ options: expect.objectContaining({ region: 'ap-southeast-2' }) })),
    )
  })
})

describe('ProviderEdit bedrock credential panel', () => {
  it('renders two labeled key inputs instead of a generic key field', async () => {
    vi.mocked(listProviders).mockResolvedValue([bedrockProviderWithRef])
    renderPage('p1')

    expect(await screen.findByPlaceholderText('AKIA…')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('wJalrXUtnFEMI/K7MDEN...')).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('paste key')).not.toBeInTheDocument()
  })

  it('keeps the generic single key input for a non-bedrock provider', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    renderPage('p2')

    expect(await screen.findByPlaceholderText('paste key')).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('AKIA…')).not.toBeInTheDocument()
  })

  it('disables the panel Save until both access key id and secret access key are filled', async () => {
    vi.mocked(listProviders).mockResolvedValue([bedrockProviderWithRef])
    renderPage('p1')

    await screen.findByPlaceholderText('AKIA…')
    const saveButtons = screen.getAllByRole('button', { name: 'Save' })
    const panelSave = saveButtons[saveButtons.length - 1]
    expect(panelSave).toBeDisabled()

    fireEvent.change(screen.getByPlaceholderText('AKIA…'), { target: { value: 'AKIAEXAMPLE' } })
    expect(panelSave).toBeDisabled()

    fireEvent.change(screen.getByPlaceholderText('wJalrXUtnFEMI/K7MDEN...'), {
      target: { value: 'secretvalue123' },
    })
    expect(panelSave).not.toBeDisabled()
  })

  it('rotates the stored secret with a JSON blob built from the two fields, independent of the parent form', async () => {
    vi.mocked(listProviders).mockResolvedValue([bedrockProviderWithRef])
    vi.mocked(setSecret).mockResolvedValue()
    renderPage('p1')

    fireEvent.change(await screen.findByPlaceholderText('AKIA…'), {
      target: { value: 'AKIAROTATED' },
    })
    fireEvent.change(screen.getByPlaceholderText('wJalrXUtnFEMI/K7MDEN...'), {
      target: { value: 'rotatedsecret' },
    })
    const saveButtons = screen.getAllByRole('button', { name: 'Save' })
    fireEvent.click(saveButtons[saveButtons.length - 1])

    await waitFor(() => expect(setSecret).toHaveBeenCalled())
    const [ref, payload] = vi.mocked(setSecret).mock.calls[0]
    expect(ref).toBe('BEDROCK_KEY')
    expect(JSON.parse(payload)).toEqual({
      access_key_id: 'AKIAROTATED',
      secret_access_key: 'rotatedsecret',
    })
    expect(patchProvider).not.toHaveBeenCalled()
  })

  it('mentions no JSON in the bedrock credential hint copy', async () => {
    vi.mocked(listProviders).mockResolvedValue([bedrockProviderWithRef])
    renderPage('p1')

    await screen.findByPlaceholderText('AKIA…')
    expect(screen.queryByText(/JSON/)).not.toBeInTheDocument()
  })
})

describe('ProviderEdit advanced fold', () => {
  const openrouterProvider: AdminProvider = {
    id: 'p-or',
    name: 'OpenRouter',
    kind: 'api',
    driver: 'openaicompat',
    base_url: 'https://openrouter.ai/api/v1',
    default_model: 'openai/gpt-4o-mini',
    credential_ref: 'OPENROUTER_API_KEY',
    headers: {},
    enabled: true,
  }

  function advancedFold(): HTMLDetailsElement {
    return screen.getByText('Advanced', { selector: 'summary' }).closest('details')!
  }

  it('holds the operator fields and stays closed when every value is the default', async () => {
    vi.mocked(listProviders).mockResolvedValue([openrouterProvider])
    renderPage('p-or')

    await screen.findByDisplayValue('OpenRouter')
    const fold = advancedFold()
    expect(fold.open).toBe(false)
    expect(fold).toContainElement(screen.getByRole('switch', { name: 'Disable reasoning' }))
    expect(fold).toContainElement(screen.getByPlaceholderText('5m'))
    expect(fold).toContainElement(screen.getByLabelText('Base URL'))
    expect(fold).not.toContainElement(screen.getByDisplayValue('OpenRouter'))
    expect(fold).not.toContainElement(screen.getByText('Default model'))
  })

  it.each([
    ['reasoning is disabled', { options: { reasoning_effort: 'none' } }],
    ['a request timeout is set', { options: { request_timeout: '20m' } }],
    ['a LiteLLM provider is set', { options: { litellm_provider: 'openrouter' } }],
    ['the base URL differs from the preset', { base_url: 'https://openrouter.ai/api/v2' }],
  ])('opens by itself when %s', async (_name, override) => {
    vi.mocked(listProviders).mockResolvedValue([{ ...openrouterProvider, ...override }])
    renderPage('p-or')

    await screen.findByDisplayValue('OpenRouter')
    expect(advancedFold().open).toBe(true)
  })

  it('ignores a trailing slash when comparing the base URL with the preset', async () => {
    vi.mocked(listProviders).mockResolvedValue([{ ...openrouterProvider, base_url: 'https://openrouter.ai/api/v1/' }])
    renderPage('p-or')

    await screen.findByDisplayValue('OpenRouter')
    expect(advancedFold().open).toBe(false)
  })

  it('shows the base URL read-only and leaves it out of the PATCH', async () => {
    vi.mocked(listProviders).mockResolvedValue([openrouterProvider])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p-or')

    await screen.findByDisplayValue('OpenRouter')
    const baseURL = screen.getByLabelText('Base URL')
    expect(baseURL).toHaveValue('https://openrouter.ai/api/v1')
    expect(baseURL).toHaveAttribute('readonly')

    fireEvent.change(screen.getByPlaceholderText('5m'), { target: { value: '20m' } })
    fireEvent.click(formSaveButton())

    await waitFor(() => expect(patchProvider).toHaveBeenCalledTimes(1))
    expect(vi.mocked(patchProvider).mock.calls[0][1]).not.toHaveProperty('base_url')
  })

  it('keeps the bedrock region in the main form and renders no fold', async () => {
    renderPage('p1')

    const region = await screen.findByRole('combobox', { name: 'AWS region' })
    expect(region.closest('details')).toBeNull()
    expect(screen.queryByText('Advanced', { selector: 'summary' })).not.toBeInTheDocument()
  })

  it('renders no fold for a cli provider', async () => {
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    renderPage('p3')

    await screen.findByDisplayValue('Claude Code')
    expect(screen.queryByText('Advanced', { selector: 'summary' })).not.toBeInTheDocument()
  })

  it('says nothing about embeddings or image models in the models panel', async () => {
    vi.mocked(listProviders).mockResolvedValue([openrouterProvider])
    vi.mocked(availableModels).mockResolvedValue([{ id: 'openai/gpt-4o-mini', source: 'live' }])
    renderPage('p-or')

    await screen.findByText('Models your key can use · 1')
    expect(screen.queryByText(/embedding/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/image model/i)).not.toBeInTheDocument()
  })

  it('buildPatch output for a fully staged provider is unchanged', () => {
    const patch = buildPatch(
      { ...openrouterProvider, options: { anthropic_base_url: 'https://example.test', reasoning_effort: 'none' } },
      {
        name: ' OpenRouter 2 ',
        credential_ref: 'OR_KEY',
        reasoningDisabled: false,
        request_timeout: ' 20m ',
        region: 'eu-west-1',
        auth: 'ambient',
        litellm_provider: ' openrouter ',
        default_model: ' openai/gpt-4o ',
      },
    )
    expect(patch).toEqual({
      name: 'OpenRouter 2',
      credential_ref: 'OR_KEY',
      default_model: 'openai/gpt-4o',
      options: {
        anthropic_base_url: 'https://example.test',
        request_timeout: '20m',
        litellm_provider: 'openrouter',
      },
    })
  })
})
