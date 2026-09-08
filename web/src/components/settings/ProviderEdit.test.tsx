import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminProvider } from '../../api/types'
import { buildPatch, ProviderEdit } from './ProviderEdit'

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

vi.mock('../../api/client', () => ({
  availableModels: vi.fn(),
  catalogModelsForProvider: vi.fn(),
  deleteProvider: vi.fn(),
  deleteSecret: vi.fn(),
  listProviders: vi.fn(),
  listSecretBackends: vi.fn(),
  listSecretRefs: vi.fn(),
  patchProvider: vi.fn(),
  secretStatus: vi.fn(),
  setSecret: vi.fn(),
  testProvider: vi.fn(),
}))

import {
  availableModels,
  catalogModelsForProvider,
  deleteProvider,
  deleteSecret,
  listProviders,
  listSecretBackends,
  listSecretRefs,
  patchProvider,
  secretStatus,
  setSecret,
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
  vi.mocked(catalogModelsForProvider).mockResolvedValue([])
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
      litellm_provider: '',
      default_model: '',
    })
    expect(patch.options).toEqual({ region: 'eu-west-1' })
  })

  it('omits region for a non-bedrock provider', () => {
    const patch = buildPatch(openaicompatProvider, {
      name: 'Ollama',
      credential_ref: '',
      reasoningDisabled: false,
      request_timeout: '',
      region: 'eu-west-1',
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

describe('ProviderEdit credential panel clear/save', () => {
  const withRef = { ...openaicompatProvider, credential_ref: 'OLLAMA_API_KEY' }

  it('clears a configured key and refreshes secret status', async () => {
    vi.mocked(listProviders).mockResolvedValue([withRef])
    vi.mocked(secretStatus).mockResolvedValue({ configured: true, backend: 'db' })
    vi.mocked(deleteSecret).mockResolvedValue()
    renderPage('p2')

    const clearButton = await screen.findByRole('button', { name: 'clear' })
    fireEvent.click(clearButton)

    await waitFor(() => expect(deleteSecret).toHaveBeenCalledWith('OLLAMA_API_KEY'))
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

  it('picking a suggestion stages it, and Save sends the PATCH', async () => {
    vi.mocked(catalogModelsForProvider).mockResolvedValue([
      {
        id: 'amazon.nova-lite-v1:0',
        model_key: 'amazon.nova-lite-v1:0',
        litellm_provider: 'bedrock',
        mode: 'chat',
        input_per_mtok: 0.06,
        output_per_mtok: 0.24,
      },
    ])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage()

    const input = await screen.findByPlaceholderText('model id')
    fireEvent.focus(input)
    fireEvent.change(input, { target: { value: 'nova-lite' } })

    fireEvent.click(await screen.findByRole('option', { name: /amazon\.nova-lite-v1:0/ }))
    expect(patchProvider).not.toHaveBeenCalled()

    fireEvent.click(formSaveButton())
    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', expect.objectContaining({ default_model: 'amazon.nova-lite-v1:0' })),
    )
  })

  it('omits the section for a kind=cli provider (it gets its own alias picker instead)', async () => {
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    renderPage('p3')

    await screen.findByDisplayValue('Claude Code')
    expect(screen.getAllByPlaceholderText('sonnet')).toHaveLength(1)
    expect(screen.queryByPlaceholderText('model id')).not.toBeInTheDocument()
  })
})

describe('ProviderEdit catalog models table', () => {
  it('renders every fetched catalog model with a price, no interactive controls', async () => {
    vi.mocked(catalogModelsForProvider).mockResolvedValue([
      {
        id: 'amazon.nova-lite-v1:0',
        model_key: 'amazon.nova-lite-v1:0',
        litellm_provider: 'bedrock',
        mode: 'chat',
        max_input_tokens: 300000,
        input_per_mtok: 0.06,
        output_per_mtok: 0.24,
      },
      {
        id: 'amazon.titan-embed-text-v1',
        model_key: 'amazon.titan-embed-text-v1',
        litellm_provider: 'bedrock',
        mode: 'embedding',
      },
    ])
    renderPage()

    expect(await screen.findByText('amazon.nova-lite-v1:0')).toBeInTheDocument()
    expect(screen.getByText('in $0.06 · out $0.24 /MTok')).toBeInTheDocument()
    expect(screen.getByText('300k ctx')).toBeInTheDocument()
    expect(screen.getByText('amazon.titan-embed-text-v1')).toBeInTheDocument()
    expect(screen.getByText('unpriced')).toBeInTheDocument()

    expect(screen.queryAllByRole('checkbox')).toHaveLength(0)
    expect(screen.queryByRole('button', { name: 'Add' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /remove/i })).not.toBeInTheDocument()
  })

  it('shows a message when the catalog has no matching models', async () => {
    vi.mocked(catalogModelsForProvider).mockResolvedValue([])
    renderPage()

    expect(await screen.findByText('no catalog models found')).toBeInTheDocument()
  })

  it('debounces the filter box into a single catalog search call with the typed query', async () => {
    vi.mocked(catalogModelsForProvider).mockResolvedValue([])
    renderPage()

    const filter = await screen.findByPlaceholderText('filter by model id…')
    fireEvent.change(filter, { target: { value: 'nov' } })
    fireEvent.change(filter, { target: { value: 'nova' } })

    await waitFor(() => expect(catalogModelsForProvider).toHaveBeenCalledWith('p1', 'nova', 200))
    expect(catalogModelsForProvider).not.toHaveBeenCalledWith('p1', 'nov', 200)
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
    vi.mocked(catalogModelsForProvider).mockResolvedValue([])
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

  it('adds a catalog model not already among the CLI aliases to the suggestion list', async () => {
    vi.mocked(catalogModelsForProvider).mockResolvedValue([
      {
        id: 'claude-opus-4-6-20261015',
        model_key: 'claude-opus-4-6-20261015',
        litellm_provider: 'anthropic',
        mode: 'chat',
      },
    ])
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    renderPage('p3')

    const input = await screen.findByPlaceholderText('sonnet')
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
