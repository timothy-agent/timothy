import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminProvider, ProviderHealth } from '../../api/types'
import { ProvidersList } from './ProvidersList'

vi.mock('../../api/client', () => ({
  listProviders: vi.fn(),
  patchProvider: vi.fn(),
  providersHealth: vi.fn(),
  testProvider: vi.fn(),
}))

import { listProviders, providersHealth } from '../../api/client'

const apiProvider: AdminProvider = {
  id: 'p1',
  name: 'OpenAI',
  kind: 'api',
  driver: 'openaicompat',
  base_url: 'https://api.openai.com/v1',
  default_model: 'gpt-4o-mini',
  models: [{ id: 'gpt-4o-mini' }],
  credential_ref: 'OPENAI_API_KEY',
  headers: {},
  enabled: true,
}

// kind='cli' rows (D-051) have no chat driver to probe, so the
// gateway's healthy map for them means "the last delegated executor
// run succeeded" (or, before any run, "credential_ref resolves") —
// not a live connection check like an api row gets.
const cliProvider: AdminProvider = {
  id: 'p2',
  name: 'Claude Code',
  kind: 'cli',
  driver: 'claude-cli',
  base_url: '',
  default_model: '',
  models: [],
  credential_ref: 'subscription',
  headers: {},
  enabled: true,
}

function renderPage() {
  return render(
    <MemoryRouter>
      <ProvidersList />
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
})

describe('ProvidersList cli row rendering', () => {
  it('shows auth failed when the last harness run failed auth', async () => {
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    vi.mocked(providersHealth).mockResolvedValue([
      { name: 'Claude Code', enabled: true, healthy: false } as ProviderHealth,
    ])
    renderPage()

    await screen.findByText('Claude Code')
    expect(screen.getByText(/subscription/)).toBeInTheDocument()
    expect(screen.getByText(/auth failed/)).toBeInTheDocument()
    expect(screen.queryByText('credential missing')).not.toBeInTheDocument()
  })

  it('shows healthy when the last harness run succeeded', async () => {
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    vi.mocked(providersHealth).mockResolvedValue([
      { name: 'Claude Code', enabled: true, healthy: true } as ProviderHealth,
    ])
    renderPage()

    await screen.findByText('Claude Code')
    expect(screen.getByText(/subscription/)).toBeInTheDocument()
    expect(screen.getByText(/healthy/)).toBeInTheDocument()
  })

  it('hides the Test button for a cli row but keeps Manage', async () => {
    vi.mocked(listProviders).mockResolvedValue([cliProvider])
    vi.mocked(providersHealth).mockResolvedValue([])
    renderPage()

    await screen.findByText('Claude Code')
    expect(screen.queryByRole('button', { name: 'Test' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Manage' })).toBeInTheDocument()
  })

  it('still renders health and Test for an api row', async () => {
    vi.mocked(listProviders).mockResolvedValue([apiProvider])
    vi.mocked(providersHealth).mockResolvedValue([
      { name: 'OpenAI', enabled: true, healthy: true } as ProviderHealth,
    ])
    renderPage()

    await waitFor(() => expect(screen.getByText('healthy')).toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Test' })).toBeInTheDocument()
  })
})
