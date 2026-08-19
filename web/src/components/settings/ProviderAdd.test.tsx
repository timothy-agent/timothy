import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChatError } from '../../api/client'
import type { AdminProvider } from '../../api/types'
import { ProviderAdd } from './ProviderAdd'

vi.mock('../../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../api/client')>()
  return {
    ...actual,
    createProvider: vi.fn(),
    listProviders: vi.fn(),
    listSecretBackends: vi.fn(),
    listSecretRefs: vi.fn(),
    searchCatalog: vi.fn(),
    setSecret: vi.fn(),
    validateProvider: vi.fn(),
  }
})

import {
  createProvider,
  listProviders,
  listSecretBackends,
  listSecretRefs,
  searchCatalog,
  setSecret,
  validateProvider,
} from '../../api/client'

// Both openaicompat, but different endpoints — models declared on one
// must never suggest for the other.
const glm: AdminProvider = {
  id: 'p1',
  name: 'GLM (Z.ai)',
  kind: 'api',
  driver: 'openaicompat',
  base_url: 'https://api.z.ai/api/paas/v4',
  default_model: 'glm-5.2',
  models: [{ id: 'glm-5.2' }],
  credential_ref: 'ZAI_API_KEY',
  headers: {},
  enabled: true,
}

function renderPage(presetId: string) {
  return render(
    <MemoryRouter initialEntries={[`/settings/providers/new/${presetId}`]}>
      <Routes>
        <Route path="/settings/providers/new/:presetId" element={<ProviderAdd />} />
      </Routes>
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  // jsdom lacks scrollIntoView; Radix Select calls it on open.
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listProviders).mockResolvedValue([glm])
  vi.mocked(listSecretBackends).mockResolvedValue([
    { backend: 'db', configured: true, default: true },
  ])
  vi.mocked(listSecretRefs).mockResolvedValue([])
  vi.mocked(searchCatalog).mockResolvedValue([])
})

describe('ProviderAdd model suggestions', () => {
  it('does not suggest another openaicompat provider\'s models when the base_url differs', async () => {
    vi.mocked(searchCatalog).mockResolvedValue([
      { id: 'grok-4.5', model_key: 'xai/grok-4.5', litellm_provider: 'xai', mode: 'chat' },
    ])
    renderPage('grok')
    const input = await screen.findByPlaceholderText('model id')
    fireEvent.focus(input)

    expect(await screen.findByText('grok-4.5')).toBeInTheDocument()
    expect(screen.queryByText('glm-5.2')).not.toBeInTheDocument()
  })

  it('suggests a matching provider\'s models when driver AND base_url both match', async () => {
    vi.mocked(searchCatalog).mockResolvedValue([
      { id: 'glm-4.7-flash', model_key: 'zai/glm-4.7-flash', litellm_provider: 'zai', mode: 'chat' },
    ])
    renderPage('glm')
    const input = await screen.findByPlaceholderText('model id')
    fireEvent.focus(input)

    expect(await screen.findByText('glm-4.7-flash')).toBeInTheDocument()
  })

  it('shows the catalog price on a priced suggestion row', async () => {
    vi.mocked(searchCatalog).mockResolvedValue([
      {
        id: 'glm-4.7-flash-air',
        model_key: 'zai/glm-4.7-flash-air',
        litellm_provider: 'zai',
        mode: 'chat',
        input_per_mtok: 0.15,
        output_per_mtok: 0.6,
      },
    ])
    renderPage('glm')
    const input = await screen.findByPlaceholderText('model id')
    fireEvent.focus(input)

    expect(await screen.findByText('in $0.15 · out $0.60 /MTok')).toBeInTheDocument()
  })

  it('shows a declared model\'s own configured price', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      { ...glm, models: [{ id: 'glm-5.2', prices: { input_per_mtok: 0.5, output_per_mtok: 1.5 } }] },
    ])
    renderPage('glm')
    const input = await screen.findByPlaceholderText('model id')
    fireEvent.change(input, { target: { value: '' } })
    fireEvent.focus(input)

    expect(await screen.findByText('in $0.50 · out $1.50 /MTok')).toBeInTheDocument()
  })

  it('falls back to the catalog price for a declared model with no configured price', async () => {
    vi.mocked(searchCatalog).mockResolvedValue([
      { id: 'glm-5.2', model_key: 'zai/glm-5.2', litellm_provider: 'zai', mode: 'chat', input_per_mtok: 0.15, output_per_mtok: 0.6 },
    ])
    renderPage('glm')
    const input = await screen.findByPlaceholderText('model id')
    fireEvent.change(input, { target: { value: '' } })
    fireEvent.focus(input)

    expect(await screen.findByText('in $0.15 · out $0.60 /MTok')).toBeInTheDocument()
  })

  it('falls back to a provider-prefixed catalog key by its last segment', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      { ...glm, name: 'Grok (xAI)', base_url: 'https://api.x.ai/v1', default_model: 'grok-2', models: [{ id: 'grok-2' }] },
    ])
    vi.mocked(searchCatalog).mockResolvedValue([
      { id: 'grok-2', model_key: 'xai/grok-2', litellm_provider: 'xai', mode: 'chat', input_per_mtok: 2, output_per_mtok: 10 },
    ])
    renderPage('grok')
    const input = await screen.findByPlaceholderText('model id')
    fireEvent.change(input, { target: { value: '' } })
    fireEvent.focus(input)

    // The declared "grok-2" (priced via the catalog fallback under
    // test) and the catalog row's own server-stripped id share the same
    // id, so they dedupe into a single suggestion row.
    expect(await screen.findAllByText('in $2 · out $10 /MTok')).toHaveLength(1)
    expect(screen.getByRole('option', { name: /^grok-2/ })).toHaveTextContent('in $2 · out $10 /MTok')
  })

  it('debounces typing into a single catalog search call with the typed q', async () => {
    renderPage('glm')
    const input = await screen.findByPlaceholderText('model id')
    fireEvent.change(input, { target: { value: 'glm-4' } })
    fireEvent.change(input, { target: { value: 'glm-4.7' } })

    await waitFor(() => expect(searchCatalog).toHaveBeenCalledWith('glm-4.7', 'zai'))
    expect(searchCatalog).not.toHaveBeenCalledWith('glm-4', 'zai')
  })

  it('picking a namespaced catalog row commits the server-stripped local id, not model_key', async () => {
    vi.mocked(searchCatalog).mockResolvedValue([
      { id: 'glm-4.5', model_key: 'zai/glm-4.5', litellm_provider: 'zai', mode: 'chat' },
    ])
    renderPage('glm')
    const input = await screen.findByPlaceholderText('model id')
    fireEvent.change(input, { target: { value: '' } })
    fireEvent.focus(input)

    fireEvent.click(await screen.findByRole('option', { name: /^glm-4\.5/ }))
    expect(input).toHaveValue('glm-4.5')
  })
})

describe('ProviderAdd seeds options.litellm_provider', () => {
  beforeEach(() => {
    vi.mocked(validateProvider).mockResolvedValue({ ok: true, latency_ms: 12, model: 'glm-5.2' })
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createProvider).mockResolvedValue('p-new')
  })

  it('seeds the preset->litellm_provider mapping on create', async () => {
    renderPage('glm')
    fireEvent.change(await screen.findByPlaceholderText('paste key'), { target: { value: 'zai-key' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await screen.findByText(/^OK,/)

    fireEvent.click(screen.getByRole('button', { name: 'Add provider' }))
    await waitFor(() => expect(createProvider).toHaveBeenCalled())
    expect(vi.mocked(createProvider).mock.calls[0][0]).toMatchObject({
      options: { litellm_provider: 'zai' },
    })
  })

  it('leaves options.litellm_provider unset for the custom preset', async () => {
    renderPage('custom')
    fireEvent.change(await screen.findByPlaceholderText('my-gateway'), { target: { value: 'my-custom' } })
    fireEvent.change(screen.getByPlaceholderText('paste key'), { target: { value: 'custom-key' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await screen.findByText(/^OK,/)

    fireEvent.click(screen.getByRole('button', { name: 'Add provider' }))
    await waitFor(() => expect(createProvider).toHaveBeenCalled())
    const call = vi.mocked(createProvider).mock.calls[0][0]
    expect(call.options?.litellm_provider).toBeUndefined()
  })
})

describe('ProviderAdd key hint links', () => {
  it('links to the provider\'s key-creation page', async () => {
    renderPage('glm')
    const link = await screen.findByRole('link', { name: 'Open GLM (Z.ai) →' })
    expect(link).toHaveAttribute('href', 'https://z.ai/manage-apikey/apikey-list')
    expect(link).toHaveAttribute('target', '_blank')
  })

  it('renders no link for a preset without a keyURL', async () => {
    renderPage('ollama')
    expect(screen.queryByRole('link', { name: /Open Ollama/ })).not.toBeInTheDocument()
  })
})

describe('ProviderAdd existing-credential picker', () => {
  beforeEach(() => {
    vi.mocked(validateProvider).mockResolvedValue({ ok: true, latency_ms: 12, model: 'glm-5.2' })
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createProvider).mockResolvedValue('p-new')
    vi.mocked(listSecretRefs).mockResolvedValue([
      { name: 'ZAI_API_KEY', referenced_by: [{ kind: 'provider', name: 'GLM (Z.ai)', role: 'credential' }] },
    ])
  })

  it('defaults to New credential with the paste field visible', async () => {
    renderPage('glm')
    expect(await screen.findByLabelText('API key')).toBeInTheDocument()
    expect(screen.queryByLabelText('existing credential')).not.toBeInTheDocument()
  })

  it('switching to Use existing swaps in the ref picker and lists stored refs', async () => {
    renderPage('glm')
    await screen.findByLabelText('API key')

    fireEvent.click(screen.getByRole('button', { name: 'Use existing' }))

    expect(screen.queryByLabelText('API key')).not.toBeInTheDocument()
    const select = await screen.findByLabelText('existing credential')
    expect(select).toBeInTheDocument()
    fireEvent.click(select)
    expect(await screen.findByRole('option', { name: /ZAI_API_KEY/ })).toBeInTheDocument()
  })

  it('choosing an existing ref sets credential_ref and skips the secret write on submit', async () => {
    renderPage('glm')
    await screen.findByLabelText('API key')
    fireEvent.click(screen.getByRole('button', { name: 'Use existing' }))

    fireEvent.click(await screen.findByLabelText('existing credential'))
    fireEvent.click(await screen.findByRole('option', { name: /ZAI_API_KEY/ }))

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(validateProvider).toHaveBeenCalled())
    expect(setSecret).not.toHaveBeenCalled()
    expect(vi.mocked(validateProvider).mock.calls[0][0]).toMatchObject({ credential_ref: 'ZAI_API_KEY' })

    fireEvent.click(await screen.findByRole('button', { name: 'Add provider' }))
    await waitFor(() => expect(createProvider).toHaveBeenCalled())
    expect(setSecret).not.toHaveBeenCalled()
    expect(vi.mocked(createProvider).mock.calls[0][0]).toMatchObject({ credential_ref: 'ZAI_API_KEY' })
  })
})

describe('ProviderAdd bedrock credential inputs', () => {
  beforeEach(() => {
    vi.mocked(validateProvider).mockResolvedValue({ ok: true, latency_ms: 12, model: 'amazon.nova-lite-v1:0' })
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createProvider).mockResolvedValue('p-bedrock')
  })

  it('renders two labeled key inputs instead of a generic key field', async () => {
    renderPage('bedrock')

    expect(await screen.findByPlaceholderText('AKIA…')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('wJalrXUtnFEMI/K7MDEN...')).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('paste key')).not.toBeInTheDocument()
  })

  it('blocks the test when the secret access key is missing', async () => {
    renderPage('bedrock')

    fireEvent.change(await screen.findByPlaceholderText('AKIA…'), {
      target: { value: 'AKIAEXAMPLE' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    expect(
      await screen.findByText('An access key ID and secret access key are required to test this provider.'),
    ).toBeInTheDocument()
    expect(setSecret).not.toHaveBeenCalled()
  })

  it('sends a valid JSON secret built from the two fields and adds the provider', async () => {
    renderPage('bedrock')

    fireEvent.change(await screen.findByPlaceholderText('AKIA…'), {
      target: { value: 'AKIAEXAMPLE' },
    })
    fireEvent.change(screen.getByPlaceholderText('wJalrXUtnFEMI/K7MDEN...'), {
      target: { value: 'secretvalue123' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    await waitFor(() => expect(setSecret).toHaveBeenCalled())
    const [ref, payload] = vi.mocked(setSecret).mock.calls[0]
    expect(ref).toBeTruthy()
    expect(JSON.parse(payload)).toEqual({
      access_key_id: 'AKIAEXAMPLE',
      secret_access_key: 'secretvalue123',
    })

    await screen.findByText(/^OK,/)
    fireEvent.click(screen.getByRole('button', { name: 'Add provider' }))

    await waitFor(() => expect(createProvider).toHaveBeenCalled())
  })

  it('mentions no JSON in the bedrock key hint copy', async () => {
    renderPage('bedrock')
    await screen.findByPlaceholderText('AKIA…')
    expect(screen.queryByText(/JSON/)).not.toBeInTheDocument()
  })

  it('still renders the generic single key input for a non-bedrock preset', async () => {
    renderPage('glm')

    expect(await screen.findByPlaceholderText('paste key')).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('AKIA…')).not.toBeInTheDocument()
  })
})

describe('ProviderAdd anthropic auth folding', () => {
  beforeEach(() => {
    vi.mocked(validateProvider).mockResolvedValue({ ok: true, latency_ms: 12, model: 'claude-haiku-4-5' })
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createProvider).mockResolvedValue('p-anthropic')
  })

  it('defaults to API key mode with the unchanged api flow', async () => {
    renderPage('anthropic')

    expect(await screen.findByPlaceholderText('sk-ant-…')).toBeInTheDocument()
    expect(screen.queryByText('CLI providers have no connection test.')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Test connection' })).toBeInTheDocument()

    fireEvent.change(screen.getByPlaceholderText('sk-ant-…'), { target: { value: 'sk-ant-api03-metered' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    await waitFor(() => expect(setSecret).toHaveBeenCalledWith(expect.any(String), 'sk-ant-api03-metered'))
    await screen.findByText(/^OK,/)
    fireEvent.click(screen.getByRole('button', { name: 'Add provider' }))

    await waitFor(() => expect(createProvider).toHaveBeenCalled())
    const call = vi.mocked(createProvider).mock.calls[0][0]
    expect(call).toMatchObject({ kind: 'api', driver: 'anthropic' })
  })

  it('rejects a subscription token pasted into the API key mode', async () => {
    renderPage('anthropic')

    fireEvent.change(await screen.findByPlaceholderText('sk-ant-…'), {
      target: { value: 'sk-ant-oat01-realtoken' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    expect(await screen.findByText(/use "Subscription token" instead/)).toBeInTheDocument()
    expect(validateProvider).not.toHaveBeenCalled()
  })

  it('switching to subscription token mode skips the connection probe entirely', async () => {
    renderPage('anthropic')

    fireEvent.click(screen.getByRole('combobox'))
    fireEvent.click(await screen.findByText('Subscription token'))

    await screen.findByText('CLI providers have no connection test.')
    expect(screen.queryByRole('button', { name: 'Test connection' })).not.toBeInTheDocument()
    expect(validateProvider).not.toHaveBeenCalled()
  })

  it('accepts a subscription token and rejects anything else', async () => {
    renderPage('anthropic')

    fireEvent.click(screen.getByRole('combobox'))
    fireEvent.click(await screen.findByText('Subscription token'))

    fireEvent.change(await screen.findByPlaceholderText('sk-ant-oat…'), {
      target: { value: 'sk-ant-api03-notatoken' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add provider' }))

    expect(
      await screen.findByText(/Subscription tokens start with sk-ant-oat/),
    ).toBeInTheDocument()
    expect(createProvider).not.toHaveBeenCalled()

    fireEvent.change(screen.getByPlaceholderText('sk-ant-oat…'), {
      target: { value: 'sk-ant-oat01-realtoken' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add provider' }))

    await waitFor(() => expect(createProvider).toHaveBeenCalled())
    expect(setSecret).toHaveBeenCalledWith(expect.any(String), 'sk-ant-oat01-realtoken')
    const call = vi.mocked(createProvider).mock.calls[0][0]
    expect(call).toMatchObject({ kind: 'cli', driver: 'claude-cli' })
  })

  it('prefills a default model for subscription token mode and sends it in the payload', async () => {
    renderPage('anthropic')

    fireEvent.click(screen.getByRole('combobox'))
    fireEvent.click(await screen.findByText('Subscription token'))

    expect(await screen.findByPlaceholderText('claude-sonnet-4-6')).toHaveValue('claude-sonnet-4-6')

    fireEvent.change(screen.getByPlaceholderText('sk-ant-oat…'), {
      target: { value: 'sk-ant-oat01-realtoken' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add provider' }))

    await waitFor(() => expect(createProvider).toHaveBeenCalled())
    const call = vi.mocked(createProvider).mock.calls[0][0]
    expect(call).toMatchObject({ kind: 'cli', driver: 'claude-cli', default_model: 'claude-sonnet-4-6' })
  })

  it('sends an edited default model for subscription token mode', async () => {
    renderPage('anthropic')

    fireEvent.click(screen.getByRole('combobox'))
    fireEvent.click(await screen.findByText('Subscription token'))

    fireEvent.change(await screen.findByPlaceholderText('claude-sonnet-4-6'), {
      target: { value: 'opus' },
    })
    fireEvent.change(screen.getByPlaceholderText('sk-ant-oat…'), {
      target: { value: 'sk-ant-oat01-realtoken' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add provider' }))

    await waitFor(() => expect(createProvider).toHaveBeenCalled())
    const call = vi.mocked(createProvider).mock.calls[0][0]
    expect(call).toMatchObject({ default_model: 'opus' })
  })

  it('shows setup-token instructions for the subscription token mode', async () => {
    renderPage('anthropic')

    fireEvent.click(screen.getByRole('combobox'))
    fireEvent.click(await screen.findByText('Subscription token'))

    expect(await screen.findByText(/claude setup-token/)).toBeInTheDocument()
    expect(screen.getByText(/long-lived/)).toBeInTheDocument()
  })
})

describe('ProviderAdd Timothy auth failures', () => {
  it('does not paint a 401 as a failed provider probe', async () => {
    vi.mocked(setSecret).mockRejectedValue(
      new ChatError(401, 'missing or invalid bearer token', 'unauthorized'),
    )

    renderPage('glm')
    fireEvent.change(await screen.findByPlaceholderText('paste key'), { target: { value: 'zai-key' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    expect(await screen.findByText(/Not tested yet/)).toBeInTheDocument()
    expect(screen.queryByText(/Failed after 0 ms/)).not.toBeInTheDocument()
    expect(screen.queryByText(/missing or invalid bearer token/)).not.toBeInTheDocument()
    expect((screen.getByRole('button', { name: 'Add provider' }) as HTMLButtonElement).disabled).toBe(true)
    expect(validateProvider).not.toHaveBeenCalled()
  })

  it('does not paint brain\'s bearer message when the error has no status', async () => {
    vi.mocked(setSecret).mockRejectedValue(new Error('missing or invalid bearer token'))

    renderPage('glm')
    fireEvent.change(await screen.findByPlaceholderText('paste key'), { target: { value: 'zai-key' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    expect(await screen.findByText(/Not tested yet/)).toBeInTheDocument()
    expect(screen.queryByText(/Failed after 0 ms/)).not.toBeInTheDocument()
    expect(screen.queryByText(/missing or invalid bearer token/)).not.toBeInTheDocument()
  })
})
