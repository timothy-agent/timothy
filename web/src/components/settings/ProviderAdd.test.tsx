import { cleanup, fireEvent, render, screen } from '@testing-library/react'
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

import { listProviders, listSecretBackends } from '../../api/client'

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
