import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminProvider } from '../../api/types'
import { ProviderAdd } from './ProviderAdd'

vi.mock('../../api/client', () => ({
  createProvider: vi.fn(),
  listProviders: vi.fn(),
  listSecretBackends: vi.fn(),
  setSecret: vi.fn(),
  validateProvider: vi.fn(),
}))

import { createProvider, listProviders, listSecretBackends, setSecret, validateProvider } from '../../api/client'

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
})

describe('ProviderAdd model suggestions', () => {
  it('does not suggest another openaicompat provider\'s models when the base_url differs', async () => {
    renderPage('grok')
    const input = await screen.findByPlaceholderText('model id')
    fireEvent.focus(input)

    expect(await screen.findByText('grok-4.5')).toBeInTheDocument()
    expect(screen.queryByText('glm-5.2')).not.toBeInTheDocument()
  })

  it('suggests a matching provider\'s models when driver AND base_url both match', async () => {
    renderPage('glm')
    const input = await screen.findByPlaceholderText('model id')
    fireEvent.focus(input)

    expect(await screen.findByText('glm-4.7-flash')).toBeInTheDocument()
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
