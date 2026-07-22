import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminProvider } from '../../api/types'
import { ProviderEdit } from './ProviderEdit'

vi.mock('../../api/client', () => ({
  availableModels: vi.fn(),
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
  listProviders,
  listSecretBackends,
  patchProvider,
  secretStatus,
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

function renderPage() {
  return render(
    <MemoryRouter initialEntries={['/settings/providers/p1']}>
      <Routes>
        <Route path="/settings/providers/:id" element={<ProviderEdit />} />
      </Routes>
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listProviders).mockResolvedValue([bedrockProvider])
  vi.mocked(availableModels).mockRejectedValue(new Error('driver bedrock cannot list models'))
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

  it('suggests catalog model ids for bedrock, which has no live listing', async () => {
    renderPage()

    const input = await screen.findByPlaceholderText('model id')
    fireEvent.focus(input)
    fireEvent.change(input, { target: { value: 'titan-embed' } })

    expect(await screen.findByRole('option', { name: /amazon\.titan-embed-text-v1/ })).toBeTruthy()
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
