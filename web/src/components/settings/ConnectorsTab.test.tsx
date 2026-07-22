import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminConnector } from '../../api/types'
import { ConnectorsTab } from './ConnectorsTab'

vi.mock('../../api/client', () => ({
  connectorOAuthStart: vi.fn(),
  createConnector: vi.fn(),
  deleteConnector: vi.fn(),
  listConnectors: vi.fn(),
  listSecretBackends: vi.fn(),
  patchConnector: vi.fn(),
  setSecret: vi.fn(),
  testConnector: vi.fn(),
}))

import {
  connectorOAuthStart,
  createConnector,
  listConnectors,
  listSecretBackends,
  patchConnector,
  setSecret,
  testConnector,
} from '../../api/client'

const calendarConnector: AdminConnector = {
  id: 'c1',
  name: 'google-calendar',
  kind: 'google',
  config: { scopes: ['https://www.googleapis.com/auth/calendar'] },
  credential_ref: 'GOOGLE_CALENDAR_GOOGLE_OAUTH',
  enabled: true,
}

const assign = vi.fn()

function renderTab(entry = '/settings/connectors') {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/settings/connectors/*" element={<ConnectorsTab />} />
      </Routes>
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listConnectors).mockResolvedValue([calendarConnector])
  vi.mocked(listSecretBackends).mockResolvedValue([
    { backend: 'db', configured: true, default: true },
    { backend: 'vault', configured: false, default: false },
    { backend: 'asm', configured: false, default: false },
  ])
  vi.stubGlobal('location', { ...window.location, assign, origin: 'http://localhost:3300' })
})

describe('Connectors tab', () => {
  it('renders configured cards and the preset tile grid', async () => {
    renderTab()
    expect(await screen.findByText('Your connectors · 1')).toBeTruthy()
    expect(screen.getByText('calendar')).toBeTruthy()
    for (const name of ['Gmail', 'Google Calendar', 'GitHub']) {
      expect(screen.getByRole('button', { name: new RegExp(name) })).toBeTruthy()
    }
  })

  it('shows the OAuth outcome banners from the callback redirect', async () => {
    renderTab('/settings/connectors?oauth_connected=personal')
    expect(await screen.findByText(/Google account connected to “personal”/)).toBeTruthy()

    cleanup()
    renderTab('/settings/connectors?oauth_error=access_denied')
    expect(await screen.findByText(/Google connection failed: access_denied/)).toBeTruthy()
  })

  it('adds an MCP connector: secret, create disabled, test, enable', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createConnector).mockResolvedValue('c2')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    vi.mocked(patchConnector).mockResolvedValue()

    renderTab()
    fireEvent.click(await screen.findByRole('button', { name: /GitHub/ }))
    fireEvent.change(await screen.findByPlaceholderText('ghp_… or github_pat_…'), {
      target: { value: 'ghp_abc' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    const addButton = await screen.findByRole('button', { name: 'Add connector' })
    await waitFor(() => expect((addButton as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(addButton)

    await waitFor(() => expect(patchConnector).toHaveBeenCalledWith('c2', { enabled: true }))
    expect(setSecret).toHaveBeenCalledWith('GITHUB_MCP_TOKEN', 'ghp_abc')
    expect(createConnector).toHaveBeenCalledWith({
      name: 'github',
      kind: 'mcp',
      config: { endpoint: 'https://api.githubcopilot.com/mcp/' },
      credential_ref: 'GITHUB_MCP_TOKEN',
      enabled: false,
    })
  })

  it('keeps a failing MCP connector disabled and says why', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createConnector).mockResolvedValue('c2')
    vi.mocked(testConnector).mockResolvedValue({ ok: false, error: 'status 401' })

    renderTab()
    fireEvent.click(await screen.findByRole('button', { name: /GitHub/ }))
    fireEvent.change(await screen.findByPlaceholderText('ghp_… or github_pat_…'), {
      target: { value: 'bad-token' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    expect(await screen.findByText(/Connection failed: status 401/)).toBeTruthy()
    expect(patchConnector).not.toHaveBeenCalled()
    expect((screen.getByRole('button', { name: 'Add connector' }) as HTMLButtonElement).disabled).toBe(true)
    expect(createConnector).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'github', enabled: false }),
    )
  })

  it('adds a Gmail connector and hands off to Google consent', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createConnector).mockResolvedValue('c3')
    vi.mocked(connectorOAuthStart).mockResolvedValue('https://accounts.google.com/o/oauth2/v2/auth?x=1')

    renderTab()
    fireEvent.click(await screen.findByRole('button', { name: /Gmail/ }))
    fireEvent.change(await screen.findByPlaceholderText('….apps.googleusercontent.com'), {
      target: { value: 'cid.apps.googleusercontent.com' },
    })
    fireEvent.change(screen.getByPlaceholderText('GOCSPX-…'), { target: { value: 'GOCSPX-secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save & connect Google' }))

    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith('https://accounts.google.com/o/oauth2/v2/auth?x=1'),
    )
    expect(setSecret).toHaveBeenCalledWith('GMAIL_GOOGLE_CLIENT_SECRET', 'GOCSPX-secret')
    expect(createConnector).toHaveBeenCalledWith(
      expect.objectContaining({
        name: 'gmail',
        kind: 'google',
        credential_ref: 'GMAIL_GOOGLE_OAUTH',
        config: expect.objectContaining({
          client_id: 'cid.apps.googleusercontent.com',
          client_secret_ref: 'GMAIL_GOOGLE_CLIENT_SECRET',
        }),
      }),
    )
    expect(connectorOAuthStart).toHaveBeenCalledWith('c3')
  })
})
