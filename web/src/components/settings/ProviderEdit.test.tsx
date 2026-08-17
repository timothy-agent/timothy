import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminProvider } from '../../api/types'
import { ProviderEdit } from './ProviderEdit'

vi.mock('../../api/client', () => ({
  availableModels: vi.fn(),
  catalogModelsForProvider: vi.fn(),
  catalogSuggestions: vi.fn(),
  deleteProvider: vi.fn(),
  deleteSecret: vi.fn(),
  listProviders: vi.fn(),
  listSecretBackends: vi.fn(),
  patchProvider: vi.fn(),
  secretStatus: vi.fn(),
  setSecret: vi.fn(),
  testProvider: vi.fn(),
}))

import {
  availableModels,
  catalogModelsForProvider,
  catalogSuggestions,
  listProviders,
  listSecretBackends,
  patchProvider,
  secretStatus,
  setSecret,
} from '../../api/client'

const bedrockProvider: AdminProvider = {
  id: 'p1',
  name: 'AWS Bedrock',
  kind: 'api',
  driver: 'bedrock',
  base_url: 'us-east-1',
  default_model: '',
  models: [],
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
  models: [],
  credential_ref: 'subscription',
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
  models: [{ id: 'qwen3' }],
  credential_ref: '',
  headers: {},
  enabled: true,
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
  vi.mocked(availableModels).mockRejectedValue(new Error('driver bedrock cannot list models'))
  vi.mocked(catalogModelsForProvider).mockResolvedValue([])
  vi.mocked(secretStatus).mockResolvedValue({ configured: false, backend: '' })
  vi.mocked(listSecretBackends).mockResolvedValue([
    { backend: 'db', configured: true, default: true },
  ])
})

describe('ProviderEdit models section', () => {
  it('adds a plain model without capabilities and sets it as default', async () => {
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage()

    const input = await screen.findByPlaceholderText('model id')
    fireEvent.change(input, { target: { value: 'us.amazon.nova-pro-v1:0' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', {
        models: [{ id: 'us.amazon.nova-pro-v1:0' }],
        default_model: 'us.amazon.nova-pro-v1:0',
      }),
    )
  })

  it('carries catalog pricing onto a model that matches a priced catalog entry', async () => {
    vi.mocked(patchProvider).mockResolvedValue()
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
    renderPage()

    const input = await screen.findByPlaceholderText('model id')
    fireEvent.change(input, { target: { value: 'amazon.nova-lite-v1:0' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', {
        models: [
          {
            id: 'amazon.nova-lite-v1:0',
            prices: { input_per_mtok: 0.06, output_per_mtok: 0.24 },
          },
        ],
        default_model: 'amazon.nova-lite-v1:0',
      }),
    )
  })

  it('adds an embeddings model with the capability flag and leaves default_model untouched', async () => {
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage()

    const input = await screen.findByPlaceholderText('model id')
    fireEvent.change(input, { target: { value: 'amazon.titan-embed-text-v1' } })
    fireEvent.click(screen.getByRole('checkbox', { name: /Embeddings model/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', {
        models: [{ id: 'amazon.titan-embed-text-v1', capabilities: ['embeddings'] }],
      }),
    )
  })

  it('adds a vision model with the capability flag and keeps it as default', async () => {
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage()

    const input = await screen.findByPlaceholderText('model id')
    fireEvent.change(input, { target: { value: 'amazon.nova-pro-vision' } })
    fireEvent.click(screen.getByRole('checkbox', { name: /Vision/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', {
        models: [{ id: 'amazon.nova-pro-vision', capabilities: ['vision'] }],
        default_model: 'amazon.nova-pro-vision',
      }),
    )
  })

  it('combines embeddings and vision flags into one capabilities array', async () => {
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage()

    const input = await screen.findByPlaceholderText('model id')
    fireEvent.change(input, { target: { value: 'combo-model' } })
    fireEvent.click(screen.getByRole('checkbox', { name: /Embeddings model/ }))
    fireEvent.click(screen.getByRole('checkbox', { name: /Vision/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', {
        models: [{ id: 'combo-model', capabilities: ['embeddings', 'vision'] }],
      }),
    )
  })

  it('attaches catalog pricing to a live-listing id instead of dropping the priced row', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(availableModels).mockResolvedValue([{ id: 'gpt-5.6-sol' }])
    vi.mocked(catalogModelsForProvider).mockResolvedValue([
      {
        id: 'gpt-5.6-sol',
        model_key: 'gpt-5.6-sol',
        litellm_provider: 'openai',
        mode: 'chat',
        input_per_mtok: 1.25,
        output_per_mtok: 10,
      },
    ])
    renderPage('p2')

    const input = await screen.findByPlaceholderText('model id')
    fireEvent.change(input, { target: { value: 'gpt-5.6-sol' } })
    fireEvent.focus(input)

    expect(await screen.findByText('in $1.25 · out $10 /MTok')).toBeInTheDocument()
  })

  it('shows a vision badge on declared vision models', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      {
        ...bedrockProvider,
        default_model: 'us.amazon.nova-pro-v1:0',
        models: [{ id: 'us.amazon.nova-pro-v1:0', capabilities: ['vision'] }],
      },
    ])
    renderPage()

    expect(await screen.findByText('us.amazon.nova-pro-v1:0')).toBeTruthy()
    expect(screen.getByText('vision')).toBeTruthy()
  })

  it('suggests catalog model ids for bedrock, which has no live listing', async () => {
    vi.mocked(catalogModelsForProvider).mockResolvedValue([
      { id: 'amazon.titan-embed-text-v1', model_key: 'amazon.titan-embed-text-v1', litellm_provider: 'bedrock', mode: 'embedding' },
    ])
    renderPage()

    const input = await screen.findByPlaceholderText('model id')
    fireEvent.focus(input)
    fireEvent.change(input, { target: { value: 'titan-embed' } })

    expect(await screen.findByRole('option', { name: /amazon\.titan-embed-text-v1/ })).toBeTruthy()
  })

  it('shows the catalog price on a priced suggestion row', async () => {
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
    renderPage()

    const input = await screen.findByPlaceholderText('model id')
    fireEvent.focus(input)
    fireEvent.change(input, { target: { value: 'nova-lite' } })

    expect(await screen.findByText('in $0.06 · out $0.24 /MTok')).toBeInTheDocument()
  })

  it('debounces typing into a single catalog search call with the typed q', async () => {
    vi.mocked(catalogModelsForProvider).mockResolvedValue([])
    renderPage()

    const input = await screen.findByPlaceholderText('model id')
    fireEvent.change(input, { target: { value: 'nov' } })
    fireEvent.change(input, { target: { value: 'nova' } })

    // Only the settled "nova" search fires — the interrupted "nov" pause
    // never reaches the 250ms debounce uninterrupted.
    await waitFor(() => expect(catalogModelsForProvider).toHaveBeenCalledWith('p1', 'nova'))
    expect(catalogModelsForProvider).not.toHaveBeenCalledWith('p1', 'nov')
  })

  it('shows an embeddings badge on declared embeddings models', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      {
        ...bedrockProvider,
        default_model: 'us.amazon.nova-pro-v1:0',
        models: [
          { id: 'us.amazon.nova-pro-v1:0' },
          { id: 'amazon.titan-embed-text-v1', capabilities: ['embeddings'] },
        ],
      },
    ])
    renderPage()

    expect(await screen.findByText('amazon.titan-embed-text-v1')).toBeTruthy()
    expect(screen.getByText('embeddings')).toBeTruthy()
  })
})

describe('ProviderEdit cli (subscription) provider', () => {
  beforeEach(() => {
    vi.mocked(availableModels).mockRejectedValue(
      new Error('provider not in the serving snapshot'),
    )
  })

  it('shows the current default model in the picker, not an editable declared-models list', async () => {
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    renderPage('p3')

    await screen.findByText('Claude Code')
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

  it('hides the Test connection button since there is no chat driver to probe', async () => {
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    renderPage('p3')

    await screen.findByText('Claude Code')
    expect(screen.queryByRole('button', { name: 'Test connection' })).not.toBeInTheDocument()
  })
})

describe('ProviderEdit reasoning section', () => {
  beforeEach(() => {
    vi.mocked(availableModels).mockResolvedValue([])
  })

  it('omits the reasoning section for non-openaicompat drivers', async () => {
    vi.mocked(listProviders).mockResolvedValue([bedrockProvider])
    renderPage('p1')

    await screen.findByText('AWS Bedrock')
    expect(screen.queryByRole('switch', { name: 'Disable reasoning' })).toBeNull()
  })

  it('writes options.reasoning_effort = "none" when the toggle is switched on', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p2')

    const toggle = await screen.findByRole('switch', { name: 'Disable reasoning' })
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    fireEvent.click(toggle)

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p2', { options: { reasoning_effort: 'none' } }),
    )
  })

  it('omits reasoning_effort entirely when the toggle is switched back off', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      { ...openaicompatProvider, options: { reasoning_effort: 'none' } },
    ])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p2')

    const toggle = await screen.findByRole('switch', { name: 'Disable reasoning' })
    expect(toggle.getAttribute('aria-checked')).toBe('true')
    fireEvent.click(toggle)

    await waitFor(() => expect(patchProvider).toHaveBeenCalledWith('p2', { options: {} }))
  })

  it('writes options.request_timeout on blur', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p2')

    const input = await screen.findByPlaceholderText('5m')
    fireEvent.change(input, { target: { value: '20m' } })
    fireEvent.blur(input)

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p2', { options: { request_timeout: '20m' } }),
    )
  })

  it('omits request_timeout entirely when cleared', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      { ...openaicompatProvider, options: { request_timeout: '20m' } },
    ])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p2')

    const input = await screen.findByPlaceholderText('5m')
    expect((input as HTMLInputElement).value).toBe('20m')
    fireEvent.change(input, { target: { value: '' } })
    fireEvent.blur(input)

    await waitFor(() => expect(patchProvider).toHaveBeenCalledWith('p2', { options: {} }))
  })

  it('saves request_timeout on Enter, not just blur', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p2')

    const input = await screen.findByPlaceholderText('5m')
    fireEvent.change(input, { target: { value: '45m' } })
    // jsdom doesn't blur on Enter by itself — the component calls
    // currentTarget.blur() itself, which real browsers do too; fire the
    // resulting blur here to observe that save path (not onBlur directly).
    fireEvent.keyDown(input, { key: 'Enter' })
    fireEvent.blur(input)

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p2', { options: { request_timeout: '45m' } }),
    )
  })

  it('resyncs the displayed request_timeout after a sibling field refetches the provider', async () => {
    vi.mocked(listProviders)
      .mockResolvedValueOnce([openaicompatProvider])
      .mockResolvedValueOnce([{ ...openaicompatProvider, options: { request_timeout: '30m' } }])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p2')

    const input = (await screen.findByPlaceholderText('5m')) as HTMLInputElement
    expect(input.value).toBe('')

    // A different field's save (e.g. the reasoning toggle) triggers the
    // same refresh() this section relies on — it must pick up the new
    // provider.options.request_timeout, not keep showing stale state.
    const toggle = await screen.findByRole('switch', { name: 'Disable reasoning' })
    fireEvent.click(toggle)

    await waitFor(() => expect(input.value).toBe('30m'))
  })
})

describe('ProviderEdit catalog provider section', () => {
  it('shows the current value and saves an edit on blur', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      { ...openaicompatProvider, options: { litellm_provider: 'ollama' } },
    ])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p2')

    const input = (await screen.findByPlaceholderText('e.g. xai, zai')) as HTMLInputElement
    expect(input.value).toBe('ollama')

    fireEvent.change(input, { target: { value: 'xai' } })
    fireEvent.blur(input)

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p2', { options: { litellm_provider: 'xai' } }),
    )
  })

  it('omits litellm_provider entirely when cleared', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      { ...openaicompatProvider, options: { litellm_provider: 'ollama' } },
    ])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p2')

    const input = await screen.findByPlaceholderText('e.g. xai, zai')
    fireEvent.change(input, { target: { value: '' } })
    fireEvent.blur(input)

    await waitFor(() => expect(patchProvider).toHaveBeenCalledWith('p2', { options: {} }))
  })

  it('omits the section for a kind=cli provider', async () => {
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    renderPage('p3')

    await screen.findByText('Claude Code')
    expect(screen.queryByPlaceholderText('e.g. xai, zai')).not.toBeInTheDocument()
  })
})

describe('ProviderEdit region section', () => {
  beforeEach(() => {
    vi.mocked(availableModels).mockRejectedValue(new Error('driver bedrock cannot list models'))
  })

  it('omits the region section for non-bedrock drivers', async () => {
    vi.mocked(listProviders).mockResolvedValue([openaicompatProvider])
    renderPage('p2')

    await screen.findByText('Ollama')
    expect(screen.queryByText('Region')).toBeNull()
  })

  it('defaults the region dropdown to us-east-1 when options.region is unset', async () => {
    vi.mocked(listProviders).mockResolvedValue([bedrockProvider])
    renderPage('p1')

    expect(await screen.findByRole('combobox')).toHaveTextContent('us-east-1 (N. Virginia)')
  })

  it('shows the stored region when options.region is set', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      { ...bedrockProvider, options: { region: 'eu-west-1' } },
    ])
    renderPage('p1')

    expect(await screen.findByRole('combobox')).toHaveTextContent('eu-west-1 (Ireland)')
  })

  it('writes options.region when a new region is picked', async () => {
    vi.mocked(listProviders).mockResolvedValue([bedrockProvider])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage('p1')

    fireEvent.click(await screen.findByRole('combobox'))
    fireEvent.click(await screen.findByText('ap-southeast-2 (Sydney)'))

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', { options: { region: 'ap-southeast-2' } }),
    )
  })
})

describe('ProviderEdit bedrock credential section', () => {
  beforeEach(() => {
    vi.mocked(availableModels).mockRejectedValue(new Error('driver bedrock cannot list models'))
  })

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

  it('disables Save until both access key id and secret access key are filled', async () => {
    vi.mocked(listProviders).mockResolvedValue([bedrockProviderWithRef])
    renderPage('p1')

    await screen.findByPlaceholderText('AKIA…')
    const saveButton = screen.getByRole('button', { name: 'Save' })
    expect(saveButton).toBeDisabled()

    fireEvent.change(screen.getByPlaceholderText('AKIA…'), { target: { value: 'AKIAEXAMPLE' } })
    expect(saveButton).toBeDisabled()

    fireEvent.change(screen.getByPlaceholderText('wJalrXUtnFEMI/K7MDEN...'), {
      target: { value: 'secretvalue123' },
    })
    expect(saveButton).not.toBeDisabled()
  })

  it('rotates the stored secret with a JSON blob built from the two fields', async () => {
    vi.mocked(listProviders).mockResolvedValue([bedrockProviderWithRef])
    vi.mocked(setSecret).mockResolvedValue()
    renderPage('p1')

    fireEvent.change(await screen.findByPlaceholderText('AKIA…'), {
      target: { value: 'AKIAROTATED' },
    })
    fireEvent.change(screen.getByPlaceholderText('wJalrXUtnFEMI/K7MDEN...'), {
      target: { value: 'rotatedsecret' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(setSecret).toHaveBeenCalled())
    const [ref, payload] = vi.mocked(setSecret).mock.calls[0]
    expect(ref).toBe('BEDROCK_KEY')
    expect(JSON.parse(payload)).toEqual({
      access_key_id: 'AKIAROTATED',
      secret_access_key: 'rotatedsecret',
    })
  })

  it('mentions no JSON in the bedrock credential hint copy', async () => {
    vi.mocked(listProviders).mockResolvedValue([bedrockProviderWithRef])
    renderPage('p1')

    await screen.findByPlaceholderText('AKIA…')
    expect(screen.queryByText(/JSON/)).not.toBeInTheDocument()
  })
})

describe('ProviderEdit catalog suggestions', () => {
  beforeEach(() => {
    vi.mocked(availableModels).mockRejectedValue(new Error('driver bedrock cannot list models'))
    vi.mocked(listProviders).mockResolvedValue([
      {
        ...bedrockProvider,
        models: [{ id: 'us.amazon.nova-pro-v1:0' }, { id: 'unmatched-model' }],
      },
    ])
  })

  it('loads suggestions on open and pre-checks matched rows only', async () => {
    vi.mocked(catalogSuggestions).mockResolvedValue([
      {
        model_id: 'us.amazon.nova-pro-v1:0',
        match: 'bedrock_converse/us.amazon.nova-pro-v1:0',
        suggested_context_window: 300000,
        suggested_prices: { input_per_mtok: 0.8, output_per_mtok: 3.2 },
      },
      { model_id: 'unmatched-model' },
    ])
    renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Suggest from catalog' }))

    expect(await screen.findByText('no match')).toBeInTheDocument()
    const checkboxes = screen.getAllByRole('checkbox') as HTMLInputElement[]
    expect(checkboxes).toHaveLength(2)
    expect(checkboxes[0].checked).toBe(true)
    expect(checkboxes[0].disabled).toBe(false)
    expect(checkboxes[1].checked).toBe(false)
    expect(checkboxes[1].disabled).toBe(true)
  })

  it('applies only checked rows, leaving unmatched/unchecked models untouched', async () => {
    vi.mocked(catalogSuggestions).mockResolvedValue([
      {
        model_id: 'us.amazon.nova-pro-v1:0',
        match: 'bedrock_converse/us.amazon.nova-pro-v1:0',
        suggested_context_window: 300000,
        suggested_prices: { input_per_mtok: 0.8, output_per_mtok: 3.2 },
      },
      { model_id: 'unmatched-model' },
    ])
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Suggest from catalog' }))
    await screen.findByText('no match')
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', {
        models: [
          {
            id: 'us.amazon.nova-pro-v1:0',
            context_window: 300000,
            prices: { input_per_mtok: 0.8, output_per_mtok: 3.2 },
          },
          { id: 'unmatched-model' },
        ],
      }),
    )
  })

  it('disables Apply until at least one row is checked', async () => {
    vi.mocked(catalogSuggestions).mockResolvedValue([{ model_id: 'unmatched-model' }])
    renderPage()

    fireEvent.click(await screen.findByRole('button', { name: 'Suggest from catalog' }))
    await screen.findByText('no match')

    expect(screen.getByRole('button', { name: 'Apply' })).toBeDisabled()
  })
})
