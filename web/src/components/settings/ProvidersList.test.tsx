import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminProvider, ProviderHealth, TestResult } from '../../api/types'
import { ProvidersList } from './ProvidersList'

vi.mock('../../api/client', () => ({
  catalogStatus: vi.fn(),
  listProviders: vi.fn(),
  patchProvider: vi.fn(),
  providersHealth: vi.fn(),
  refreshCatalog: vi.fn(),
  testProvider: vi.fn(),
}))

import { catalogStatus, listProviders, providersHealth, refreshCatalog, testProvider } from '../../api/client'

const apiProvider: AdminProvider = {
  id: 'p1',
  name: 'OpenAI',
  kind: 'api',
  driver: 'openaicompat',
  base_url: 'https://api.openai.com/v1',
  default_model: 'gpt-4o-mini',
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
  vi.mocked(catalogStatus).mockResolvedValue({ fetched_at: null, entry_count: 0, error: '' })
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

  it('running Test shows Testing…, then renders the probe result and hides Test until idle again', async () => {
    vi.mocked(listProviders).mockResolvedValue([apiProvider])
    vi.mocked(providersHealth).mockResolvedValue([
      { name: 'OpenAI', enabled: true, healthy: true } as ProviderHealth,
    ])
    let resolve!: (v: TestResult) => void
    vi.mocked(testProvider).mockReturnValue(new Promise<TestResult>((r) => (resolve = r)))
    renderPage()

    const testButton = await screen.findByRole('button', { name: 'Test' })
    fireEvent.click(testButton)

    expect(await screen.findByRole('button', { name: 'Testing…' })).toBeDisabled()

    resolve({ ok: true, latency_ms: 12, model: 'gpt-4o-mini' })
    expect(await screen.findByText(/^OK,/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Test' })).not.toBeDisabled()
  })
})

describe('ProvidersList catalog status line', () => {
  beforeEach(() => {
    vi.mocked(listProviders).mockResolvedValue([])
    vi.mocked(providersHealth).mockResolvedValue([])
  })

  it('shows entry count and never-synced when fetched_at is null', async () => {
    vi.mocked(catalogStatus).mockResolvedValue({ fetched_at: null, entry_count: 0, error: '' })
    renderPage()

    expect(await screen.findByText(/Model catalog: 0 models, never synced/)).toBeInTheDocument()
  })

  it('shows a relative sync time when fetched_at is set', async () => {
    vi.mocked(catalogStatus).mockResolvedValue({
      fetched_at: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(),
      entry_count: 7432,
      error: '',
    })
    renderPage()

    expect(await screen.findByText(/Model catalog: 7,432 models, synced 2h ago/)).toBeInTheDocument()
  })

  it('shows the last error alongside the status', async () => {
    vi.mocked(catalogStatus).mockResolvedValue({
      fetched_at: null,
      entry_count: 0,
      error: 'unexpected status 500',
    })
    renderPage()

    expect(await screen.findByText(/last refresh failed: unexpected status 500/)).toBeInTheDocument()
  })

  it('refreshes on button click and shows the updated status', async () => {
    vi.mocked(catalogStatus).mockResolvedValue({ fetched_at: null, entry_count: 0, error: '' })
    vi.mocked(refreshCatalog).mockResolvedValue({
      fetched_at: new Date().toISOString(),
      entry_count: 100,
      error: '',
    })
    renderPage()

    await screen.findByText(/never synced/)
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))

    await waitFor(() => expect(refreshCatalog).toHaveBeenCalled())
    expect(await screen.findByText(/Model catalog: 100 models, synced/)).toBeInTheDocument()
  })
})
