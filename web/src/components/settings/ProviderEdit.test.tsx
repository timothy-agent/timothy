import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminProvider } from '../../api/types'
import { ProviderEdit } from './ProviderEdit'

vi.mock('../../api/client', () => ({
  catalogModelsForProvider: vi.fn(),
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
  catalogModelsForProvider,
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
  vi.mocked(secretStatus).mockResolvedValue({ configured: false, backend: '' })
  vi.mocked(listSecretBackends).mockResolvedValue([
    { backend: 'db', configured: true, default: true },
  ])
})

describe('ProviderEdit default model section', () => {
  it('shows the current default model', async () => {
    vi.mocked(listProviders).mockResolvedValue([
      { ...bedrockProvider, default_model: 'us.amazon.nova-pro-v1:0' },
    ])
    renderPage()

    expect(await screen.findByPlaceholderText('model id')).toHaveValue('us.amazon.nova-pro-v1:0')
  })

  it('commits a typed model id on blur', async () => {
    vi.mocked(patchProvider).mockResolvedValue()
    renderPage()

    const input = await screen.findByPlaceholderText('model id')
    fireEvent.change(input, { target: { value: 'us.amazon.nova-pro-v1:0' } })
    fireEvent.blur(input)

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', { default_model: 'us.amazon.nova-pro-v1:0' }),
    )
  })

  it('commits a picked catalog suggestion', async () => {
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

    await waitFor(() =>
      expect(patchProvider).toHaveBeenCalledWith('p1', { default_model: 'amazon.nova-lite-v1:0' }),
    )
  })

  it('omits the section for a kind=cli provider (it gets its own alias picker instead)', async () => {
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    renderPage('p3')

    await screen.findByText('Claude Code')
    expect(screen.getAllByPlaceholderText('sonnet')).toHaveLength(1)
    expect(screen.queryByPlaceholderText('model id')).not.toBeInTheDocument()
  })
})

describe('ProviderEdit catalog models list', () => {
  it('renders every fetched catalog model with a price label, no interactive controls', async () => {
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
    expect(screen.queryByRole('button', { name: /set.default/i })).not.toBeInTheDocument()
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
