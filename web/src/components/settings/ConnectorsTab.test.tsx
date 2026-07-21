import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminConnector } from '../../api/types'
import { ConnectorsTab } from './ConnectorsTab'

vi.mock('../../api/client', () => ({
  connectorOAuthStart: vi.fn(),
  createConnector: vi.fn(),
  deleteConnector: vi.fn(),
  listConnectors: vi.fn(),
  patchConnector: vi.fn(),
  setSecret: vi.fn(),
  testConnector: vi.fn(),
}))

import {
  connectorOAuthStart,
  createConnector,
  listConnectors,
  patchConnector,
  setSecret,
  testConnector,
} from '../../api/client'

const githubConnector: AdminConnector = {
  id: 'c1',
  name: 'github',
  kind: 'mcp',
  config: { endpoint: 'https://api.githubcopilot.com/mcp/' },
  credential_ref: 'GITHUB_MCP_TOKEN',
  enabled: true,
}

const assign = vi.fn()

function renderTab(entry = '/settings?tab=connectors') {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <ConnectorsTab />
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listConnectors).mockResolvedValue([githubConnector])
  vi.stubGlobal('location', { ...window.location, assign, origin: 'http://localhost:3300' })
})

describe('Connectors tab', () => {
  it('renders configured cards and the preset tile grid', async () => {
    renderTab()
    expect(await screen.findByText('Your connectors · 1')).toBeTruthy()
    expect(screen.getByText('https://api.githubcopilot.com/mcp/')).toBeTruthy()
    for (const name of ['Gmail', 'Google Calendar', 'Grafana', 'Custom MCP server']) {
      expect(screen.getByRole('button', { name: new RegExp(name) })).toBeTruthy()
    }
  })

  it('shows the OAuth outcome banners from the callback redirect', async () => {
    renderTab('/settings?tab=connectors&oauth_connected=personal')
    expect(await screen.findByText(/Google account connected to “personal”/)).toBeTruthy()

    cleanup()
    renderTab('/settings?tab=connectors&oauth_error=access_denied')
    expect(await screen.findByText(/Google connection failed: access_denied/)).toBeTruthy()
  })

  it('adds an MCP connector: secret, create disabled, test, enable', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createConnector).mockResolvedValue('c2')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    vi.mocked(patchConnector).mockResolvedValue()

    renderTab()
    fireEvent.click(await screen.findByRole('button', { name: /Grafana/ }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByPlaceholderText('https://…/mcp'), {
      target: { value: 'https://grafana.internal/mcp' },
    })
    fireEvent.change(dialog.getByPlaceholderText('service account token'), {
      target: { value: 'glsa_abc' },
    })
    fireEvent.click(dialog.getByRole('button', { name: 'Add & test' }))

    await waitFor(() => expect(patchConnector).toHaveBeenCalledWith('c2', { enabled: true }))
    expect(setSecret).toHaveBeenCalledWith('GRAFANA_MCP_TOKEN', 'glsa_abc')
    expect(createConnector).toHaveBeenCalledWith({
      name: 'grafana',
      kind: 'mcp',
      config: { endpoint: 'https://grafana.internal/mcp' },
      credential_ref: 'GRAFANA_MCP_TOKEN',
      enabled: false,
    })
  })

  it('keeps a failing MCP connector disabled and says why', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createConnector).mockResolvedValue('c2')
    vi.mocked(testConnector).mockResolvedValue({ ok: false, error: 'status 401' })

    renderTab()
    fireEvent.click(await screen.findByRole('button', { name: /Custom MCP server/ }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByLabelText(/Name/), { target: { value: 'my server' } })
    fireEvent.change(dialog.getByPlaceholderText('https://…/mcp'), {
      target: { value: 'https://x.example/mcp' },
    })
    fireEvent.click(dialog.getByRole('button', { name: 'Add & test' }))

    expect(await dialog.findByText(/Connection failed: status 401/)).toBeTruthy()
    expect(patchConnector).not.toHaveBeenCalled()
    // Name got slugified for the tool-name prefix.
    expect(createConnector).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'my-server', enabled: false }),
    )
  })

  it('adds a Gmail connector and hands off to Google consent', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createConnector).mockResolvedValue('c3')
    vi.mocked(connectorOAuthStart).mockResolvedValue('https://accounts.google.com/o/oauth2/v2/auth?x=1')

    renderTab()
    fireEvent.click(await screen.findByRole('button', { name: /Gmail/ }))
    const dialog = within(await screen.findByRole('dialog'))
    fireEvent.change(dialog.getByPlaceholderText('….apps.googleusercontent.com'), {
      target: { value: 'cid.apps.googleusercontent.com' },
    })
    fireEvent.change(dialog.getByPlaceholderText('GOCSPX-…'), { target: { value: 'GOCSPX-secret' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save & connect Google' }))

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
