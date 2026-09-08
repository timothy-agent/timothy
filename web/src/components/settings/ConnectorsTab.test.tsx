import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminConnector } from '../../api/types'
import { TooltipProvider } from '../ui/tooltip'
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
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import {
  connectorOAuthStart,
  createConnector,
  deleteConnector,
  listConnectors,
  listSecretBackends,
  patchConnector,
  setSecret,
  testConnector,
} from '../../api/client'
import { toast } from 'sonner'

const calendarConnector: AdminConnector = {
  id: 'c1',
  name: 'google-calendar',
  kind: 'google',
  config: { scopes: ['https://www.googleapis.com/auth/calendar'] },
  credential_ref: 'GOOGLE_CALENDAR_GOOGLE_OAUTH',
  enabled: true,
  sensitive: false,
}

const assign = vi.fn()

function renderTab(entry = '/settings/connectors') {
  return render(
    <TooltipProvider>
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path="/settings/connectors/*" element={<ConnectorsTab />} />
        </Routes>
      </MemoryRouter>
    </TooltipProvider>,
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
    // AddPresetTile's accessible name is the title alone (description
    // reachable via aria-describedby), per contract.
    for (const name of ['Gmail', 'Google Calendar', 'Google Drive', 'Google Docs', 'GitHub MCP', 'GitHub']) {
      expect(screen.getByRole('link', { name })).toBeTruthy()
    }
  })

  it('shows a sensitive badge on the list card when the connector is marked sensitive', async () => {
    vi.mocked(listConnectors).mockResolvedValue([{ ...calendarConnector, sensitive: true }])
    renderTab()
    expect(await screen.findByText('calendar')).toBeTruthy()
    expect(screen.getByText('Sensitive')).toBeTruthy()
  })

  it('does not show a sensitive badge for a non-sensitive connector', async () => {
    renderTab()
    expect(await screen.findByText('calendar')).toBeTruthy()
    expect(screen.queryByText('Sensitive')).toBeNull()
  })

  it('stages the sensitive switch: flips immediately with no PATCH, Save sends it', async () => {
    vi.mocked(patchConnector).mockResolvedValue()
    renderTab(`/settings/connectors/${calendarConnector.id}`)

    const toggle = await screen.findByRole('switch', { name: 'google-calendar sensitive' })
    fireEvent.click(toggle)

    expect(toggle).toHaveAttribute('data-state', 'checked')
    expect(patchConnector).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(patchConnector).toHaveBeenCalledWith(calendarConnector.id, {
        name: 'google-calendar',
        sensitive: true,
        config: { scopes: ['https://www.googleapis.com/auth/calendar'], sign_commits: false },
      }),
    )
  })

  it('edits the name field and Save sends a PATCH with the slugified name; header shows the old name until success', async () => {
    vi.mocked(patchConnector).mockResolvedValue()
    renderTab(`/settings/connectors/${calendarConnector.id}`)
    await screen.findByRole('heading', { name: 'google-calendar' })

    const input = screen.getByRole('textbox', { name: 'Connector name' })
    fireEvent.change(input, { target: { value: 'New Calendar' } })
    expect(patchConnector).not.toHaveBeenCalled()
    expect(screen.getByRole('heading', { name: 'google-calendar' })).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(patchConnector).toHaveBeenCalledWith(calendarConnector.id, {
        name: 'new-calendar',
        sensitive: false,
        config: { scopes: ['https://www.googleapis.com/auth/calendar'], sign_commits: false },
      }),
    )
  })

  it('Cancel restores the name with no PATCH; Rename connector button no longer exists', async () => {
    renderTab(`/settings/connectors/${calendarConnector.id}`)
    await screen.findByRole('heading', { name: 'google-calendar' })

    expect(screen.queryByRole('button', { name: 'Rename connector' })).toBeNull()

    const input = screen.getByRole('textbox', { name: 'Connector name' })
    fireEvent.change(input, { target: { value: 'New Calendar' } })
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect((screen.getByRole('textbox', { name: 'Connector name' }) as HTMLInputElement).value).toBe(
      'google-calendar',
    )
    expect(patchConnector).not.toHaveBeenCalled()
  })

  it('renders a Reconnect button and the failure message for a failed google test', async () => {
    vi.mocked(testConnector).mockResolvedValue({
      ok: false,
      error: 'Google authorization expired or was revoked — reconnect to re-authorize. (Testing-mode OAuth apps expire grants roughly weekly.)',
    })
    vi.mocked(connectorOAuthStart).mockResolvedValue('https://accounts.google.com/o/oauth2/v2/auth?x=2')

    renderTab(`/settings/connectors/${calendarConnector.id}`)
    fireEvent.click(await screen.findByRole('button', { name: 'Test connection' }))

    expect(await screen.findByText(/Failed: Google authorization expired or was revoked/)).toBeTruthy()
    const reconnect = screen.getByRole('button', { name: 'Reconnect' })
    fireEvent.click(reconnect)

    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith('https://accounts.google.com/o/oauth2/v2/auth?x=2'),
    )
    expect(connectorOAuthStart).toHaveBeenCalledWith(calendarConnector.id)
  })

  it('keeps the plain Test connection button for a non-google connector test failure', async () => {
    const githubConnector: AdminConnector = {
      id: 'gh1',
      name: 'personal-gh',
      kind: 'github',
      config: {},
      credential_ref: 'PERSONAL_GH_GITHUB_PAT',
      enabled: true,
      sensitive: false,
    }
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    vi.mocked(testConnector).mockResolvedValue({ ok: false, error: 'GitHub token invalid or expired — replace the PAT' })

    renderTab(`/settings/connectors/${githubConnector.id}`)
    fireEvent.click(await screen.findByRole('button', { name: 'Test connection' }))

    expect(await screen.findByText(/Failed: GitHub token invalid or expired/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Reconnect' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Test connection' })).toBeTruthy()
    expect(screen.getByText(/Paste a new personal access token below/)).toBeTruthy()
  })

  it('stages sign commits: switch flips immediately with no PATCH, Save patches the config; key block absent until the refetch returns it', async () => {
    const githubConnector: AdminConnector = {
      id: 'gh1',
      name: 'personal-gh',
      kind: 'github',
      config: {},
      credential_ref: 'PERSONAL_GH_GITHUB_PAT',
      enabled: true,
      sensitive: false,
    }
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    vi.mocked(patchConnector).mockResolvedValue()

    renderTab(`/settings/connectors/${githubConnector.id}`)

    const toggle = await screen.findByRole('switch', { name: 'personal-gh sign commits' })
    fireEvent.click(toggle)

    expect(toggle).toHaveAttribute('data-state', 'checked')
    expect(patchConnector).not.toHaveBeenCalled()
    expect(screen.getByText('A signing key is generated when you save.')).toBeTruthy()

    vi.mocked(listConnectors).mockResolvedValue([
      { ...githubConnector, config: { sign_commits: true, signing_public_key: 'ssh-ed25519 AAAAC3Nz… timothy' } },
    ])
    const saveButtons = screen.getAllByRole('button', { name: 'Save' })
    fireEvent.click(saveButtons.find((b) => b.getAttribute('type') === 'submit')!)

    await waitFor(() =>
      expect(patchConnector).toHaveBeenCalledWith('gh1', {
        name: 'personal-gh',
        sensitive: false,
        config: { sign_commits: true },
      }),
    )
    expect(await screen.findByDisplayValue('ssh-ed25519 AAAAC3Nz… timothy')).toBeTruthy()
    const link = screen.getByRole('link', { name: /new SSH key/ })
    expect(link.getAttribute('href')).toBe('https://github.com/settings/ssh/new')
  })

  it('does not show the public key block when sign_commits is off', async () => {
    const githubConnector: AdminConnector = {
      id: 'gh1',
      name: 'personal-gh',
      kind: 'github',
      config: {},
      credential_ref: 'PERSONAL_GH_GITHUB_PAT',
      enabled: true,
      sensitive: false,
    }
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])

    renderTab(`/settings/connectors/${githubConnector.id}`)

    await screen.findByRole('switch', { name: 'personal-gh sign commits' })
    expect(screen.queryByRole('link', { name: /new SSH key/ })).toBeNull()
  })

  it('shows the signing public key with a GitHub link when already on (loaded with the key present)', async () => {
    const githubConnector: AdminConnector = {
      id: 'gh1',
      name: 'personal-gh',
      kind: 'github',
      config: { sign_commits: true, signing_public_key: 'ssh-ed25519 AAAAC3Nz… timothy' },
      credential_ref: 'PERSONAL_GH_GITHUB_PAT',
      enabled: true,
      sensitive: false,
    }
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])

    renderTab(`/settings/connectors/${githubConnector.id}`)

    expect(await screen.findByDisplayValue('ssh-ed25519 AAAAC3Nz… timothy')).toBeTruthy()
    const link = screen.getByRole('link', { name: /new SSH key/ })
    expect(link.getAttribute('href')).toBe('https://github.com/settings/ssh/new')
    expect(screen.getByText(/Signing Key/)).toBeTruthy()
  })

  it('zero PATCH before Save, exactly one on Save; Cancel discards staged edits', async () => {
    vi.mocked(patchConnector).mockResolvedValue()
    renderTab(`/settings/connectors/${calendarConnector.id}`)

    const toggle = await screen.findByRole('switch', { name: 'google-calendar sensitive' })
    fireEvent.click(toggle)
    expect(patchConnector).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(patchConnector).not.toHaveBeenCalled()
    expect(await screen.findByRole('switch', { name: 'google-calendar sensitive' })).toHaveAttribute(
      'data-state',
      'unchecked',
    )
  })

  it('shows a persistent Alert with Retry when Save fails, and Retry re-sends', async () => {
    vi.mocked(patchConnector).mockRejectedValueOnce(new Error('server unavailable'))
    renderTab(`/settings/connectors/${calendarConnector.id}`)

    const toggle = await screen.findByRole('switch', { name: 'google-calendar sensitive' })
    fireEvent.click(toggle)
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText(/server unavailable/)).toBeTruthy()

    vi.mocked(patchConnector).mockResolvedValue()
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(patchConnector).toHaveBeenCalledTimes(2))
  })

  it('shows the OAuth outcome banners from the callback redirect', async () => {
    renderTab('/settings/connectors?oauth_connected=personal')
    expect(await screen.findByText(/Account connected to “personal”/)).toBeTruthy()

    cleanup()
    renderTab('/settings/connectors?oauth_error=access_denied')
    expect(await screen.findByText(/Connection failed: access_denied/)).toBeTruthy()
  })

  it('adds an MCP connector: secret, create disabled, test, enable', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createConnector).mockResolvedValue('c2')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    vi.mocked(patchConnector).mockResolvedValue()

    renderTab()
    fireEvent.click(await screen.findByRole('link', { name: 'GitHub MCP' }))
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
      name: 'github-mcp',
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
    fireEvent.click(await screen.findByRole('link', { name: 'GitHub MCP' }))
    fireEvent.change(await screen.findByPlaceholderText('ghp_… or github_pat_…'), {
      target: { value: 'bad-token' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    expect(await screen.findByText(/Connection failed: status 401/)).toBeTruthy()
    expect(patchConnector).not.toHaveBeenCalled()
    expect((screen.getByRole('button', { name: 'Add connector' }) as HTMLButtonElement).disabled).toBe(true)
    expect(createConnector).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'github-mcp', enabled: false }),
    )
  })

  it('adds a Gmail connector and hands off to Google consent', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createConnector).mockResolvedValue('c3')
    vi.mocked(connectorOAuthStart).mockResolvedValue('https://accounts.google.com/o/oauth2/v2/auth?x=1')

    renderTab()
    fireEvent.click(await screen.findByRole('link', { name: /Gmail/ }))
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

  it('adds a Google Drive connector with the read-only scope and hands off to Google consent', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createConnector).mockResolvedValue('c4')
    vi.mocked(connectorOAuthStart).mockResolvedValue('https://accounts.google.com/o/oauth2/v2/auth?x=3')

    renderTab()
    fireEvent.click(await screen.findByRole('link', { name: 'Google Drive' }))
    fireEvent.change(await screen.findByPlaceholderText('….apps.googleusercontent.com'), {
      target: { value: 'cid.apps.googleusercontent.com' },
    })
    fireEvent.change(screen.getByPlaceholderText('GOCSPX-…'), { target: { value: 'GOCSPX-secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save & connect Google' }))

    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith('https://accounts.google.com/o/oauth2/v2/auth?x=3'),
    )
    expect(createConnector).toHaveBeenCalledWith(
      expect.objectContaining({
        name: 'google-drive',
        kind: 'google',
        credential_ref: 'GOOGLE_DRIVE_GOOGLE_OAUTH',
        config: expect.objectContaining({
          scopes: ['https://www.googleapis.com/auth/drive.readonly'],
        }),
      }),
    )
    expect(connectorOAuthStart).toHaveBeenCalledWith('c4')
  })

  it('adds an Outlook connector and hands off to Microsoft consent', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createConnector).mockResolvedValue('c5')
    vi.mocked(connectorOAuthStart).mockResolvedValue(
      'https://login.microsoftonline.com/common/oauth2/v2.0/authorize?x=1',
    )

    renderTab()
    fireEvent.click(await screen.findByRole('link', { name: /Outlook/ }))
    fireEvent.change(await screen.findByPlaceholderText('application (client) ID'), {
      target: { value: 'msft-client-id' },
    })
    fireEvent.change(screen.getByPlaceholderText('client secret value'), { target: { value: 'msft-secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save & connect Microsoft' }))

    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith('https://login.microsoftonline.com/common/oauth2/v2.0/authorize?x=1'),
    )
    expect(setSecret).toHaveBeenCalledWith('OUTLOOK_MICROSOFT_CLIENT_SECRET', 'msft-secret')
    expect(createConnector).toHaveBeenCalledWith(
      expect.objectContaining({
        name: 'outlook',
        kind: 'microsoft',
        credential_ref: 'OUTLOOK_MICROSOFT_OAUTH',
        config: expect.objectContaining({
          client_id: 'msft-client-id',
          client_secret_ref: 'OUTLOOK_MICROSOFT_CLIENT_SECRET',
          scopes: ['Mail.Read', 'Mail.Send', 'Calendars.Read', 'offline_access', 'User.Read'],
        }),
      }),
    )
    expect(connectorOAuthStart).toHaveBeenCalledWith('c5')
  })
})

describe('ConnectorEdit load and not-found', () => {
  it('redirects to the connectors list when the id is not found', async () => {
    renderTab(`/settings/connectors/missing`)
    expect(await screen.findByText('Your connectors · 1')).toBeTruthy()
  })

  it('shows a toast when loading the connector list fails', async () => {
    vi.mocked(listConnectors).mockRejectedValue(new Error('network down'))
    renderTab(`/settings/connectors/${calendarConnector.id}`)

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not load connector', {
        description: 'network down',
      }),
    )
  })
})

describe('ConnectorEdit delete', () => {
  it('deletes behind confirm and navigates back to the list', async () => {
    vi.mocked(deleteConnector).mockResolvedValue()
    renderTab(`/settings/connectors/${calendarConnector.id}`)

    await screen.findByRole('heading', { name: 'google-calendar' })
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(deleteConnector).toHaveBeenCalledWith('c1'))
    expect(await screen.findByText('Your connectors · 1')).toBeTruthy()
  })

  it('keeps the dialog open and toasts on delete failure', async () => {
    vi.mocked(deleteConnector).mockRejectedValue(new Error('still referenced'))
    renderTab(`/settings/connectors/${calendarConnector.id}`)

    await screen.findByRole('heading', { name: 'google-calendar' })
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not remove connector', {
        description: 'still referenced',
      }),
    )
  })
})

describe('ConnectorEdit rotate token and copy key', () => {
  const githubConnector: AdminConnector = {
    id: 'gh1',
    name: 'personal-gh',
    kind: 'github',
    config: { sign_commits: true, signing_public_key: 'ssh-ed25519 AAAAC3Nz… timothy' },
    credential_ref: 'PERSONAL_GH_GITHUB_PAT',
    enabled: true,
    sensitive: false,
  }

  it('rotates the github PAT via the existing credential_ref', async () => {
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    vi.mocked(setSecret).mockResolvedValue()
    renderTab(`/settings/connectors/${githubConnector.id}`)

    const tokenInput = await screen.findByPlaceholderText('paste new token')
    expect(tokenInput).toHaveAttribute('type', 'password')
    fireEvent.change(tokenInput, { target: { value: 'ghp_new' } })
    const saveButtons = screen.getAllByRole('button', { name: 'Save' })
    fireEvent.click(saveButtons.find((b) => b.getAttribute('type') !== 'submit')!)

    await waitFor(() => expect(setSecret).toHaveBeenCalledWith('PERSONAL_GH_GITHUB_PAT', 'ghp_new'))
    expect(patchConnector).not.toHaveBeenCalled()
  })

  it('rotates an imap password and mints a credential_ref when none exists yet', async () => {
    const imapConnector: AdminConnector = {
      id: 'imap1',
      name: 'my-mail',
      kind: 'imap',
      config: { username: 'me@example.com', host: 'imap.example.com' },
      credential_ref: '',
      enabled: true,
      sensitive: false,
    }
    vi.mocked(listConnectors).mockResolvedValue([imapConnector])
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(patchConnector).mockResolvedValue()
    renderTab(`/settings/connectors/${imapConnector.id}`)

    expect(await screen.findByText('me@example.com @ imap.example.com')).toBeTruthy()
    fireEvent.change(screen.getByPlaceholderText('paste new token'), { target: { value: 'app-pass' } })
    fireEvent.click(screen.getAllByRole('button', { name: 'Save' }).find((b) => b.getAttribute('type') !== 'submit')!)

    await waitFor(() => expect(setSecret).toHaveBeenCalledWith('MY_MAIL_IMAP_PASSWORD', 'app-pass'))
    expect(patchConnector).toHaveBeenCalledWith('imap1', { credential_ref: 'MY_MAIL_IMAP_PASSWORD' })
  })

  it('rotates a caldav password and shows the username @ url summary', async () => {
    const caldavConnector: AdminConnector = {
      id: 'cal1',
      name: 'my-cal',
      kind: 'caldav',
      config: { username: 'me@example.com', url: 'https://cal.example.com/dav/' },
      credential_ref: 'MY_CAL_CALDAV_PASSWORD',
      enabled: true,
      sensitive: false,
    }
    vi.mocked(listConnectors).mockResolvedValue([caldavConnector])
    vi.mocked(setSecret).mockResolvedValue()
    renderTab(`/settings/connectors/${caldavConnector.id}`)

    expect(await screen.findByText('me@example.com @ https://cal.example.com/dav/')).toBeTruthy()
    fireEvent.change(screen.getByPlaceholderText('paste new token'), { target: { value: 'new-pass' } })
    fireEvent.click(screen.getAllByRole('button', { name: 'Save' }).find((b) => b.getAttribute('type') !== 'submit')!)

    await waitFor(() => expect(setSecret).toHaveBeenCalledWith('MY_CAL_CALDAV_PASSWORD', 'new-pass'))
  })

  it('shows a toast when rotating the token fails', async () => {
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    vi.mocked(setSecret).mockRejectedValue(new Error('store unavailable'))
    renderTab(`/settings/connectors/${githubConnector.id}`)

    fireEvent.change(await screen.findByPlaceholderText('paste new token'), { target: { value: 'ghp_x' } })
    fireEvent.click(screen.getAllByRole('button', { name: 'Save' }).find((b) => b.getAttribute('type') !== 'submit')!)

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not save token', { description: 'store unavailable' }),
    )
  })

  it('copies the signing public key to the clipboard', async () => {
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    renderTab(`/settings/connectors/${githubConnector.id}`)

    fireEvent.click(await screen.findByRole('button', { name: 'Copy' }))

    await waitFor(() => expect(writeText).toHaveBeenCalledWith('ssh-ed25519 AAAAC3Nz… timothy'))
    expect(toast.success).toHaveBeenCalledWith('Public key copied')
  })

  it('shows the endpoint summary and MCP token rotate label for a plain mcp connector', async () => {
    const mcpConnector: AdminConnector = {
      id: 'mcp1',
      name: 'custom-mcp',
      kind: 'mcp',
      config: { endpoint: 'https://mcp.example.com' },
      credential_ref: 'CUSTOM_MCP_TOKEN',
      enabled: true,
      sensitive: false,
    }
    vi.mocked(listConnectors).mockResolvedValue([mcpConnector])
    renderTab(`/settings/connectors/${mcpConnector.id}`)

    expect(await screen.findByText('Endpoint:')).toBeTruthy()
    expect(screen.getByText('https://mcp.example.com')).toBeTruthy()
    expect(screen.getByText('Rotate bearer token')).toBeTruthy()
  })
})

describe('ConnectorEdit test connection identity success', () => {
  it('shows the connected identity and scopes on a passing github test', async () => {
    const githubConnector: AdminConnector = {
      id: 'gh1',
      name: 'personal-gh',
      kind: 'github',
      config: {},
      credential_ref: 'PERSONAL_GH_GITHUB_PAT',
      enabled: true,
      sensitive: false,
    }
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    vi.mocked(testConnector).mockResolvedValue({
      ok: true,
      identity: { login: 'octocat', scopes: 'repo, workflow' },
    })
    renderTab(`/settings/connectors/${githubConnector.id}`)

    fireEvent.click(await screen.findByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText(/octocat.*repo, workflow/)).toBeTruthy()
  })

  it('re-tests from the Test connection button shown after a passing test', async () => {
    const githubConnector: AdminConnector = {
      id: 'gh1',
      name: 'personal-gh',
      kind: 'github',
      config: {},
      credential_ref: 'PERSONAL_GH_GITHUB_PAT',
      enabled: true,
      sensitive: false,
    }
    vi.mocked(listConnectors).mockResolvedValue([githubConnector])
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    renderTab(`/settings/connectors/${githubConnector.id}`)

    fireEvent.click(await screen.findByRole('button', { name: 'Test connection' }))
    await screen.findByText('Connection OK, tools are servable.')
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    await waitFor(() => expect(testConnector).toHaveBeenCalledTimes(2))
  })

  it('does not surface a manage-page test result on a Timothy auth error', async () => {
    renderTab(`/settings/connectors/${calendarConnector.id}`)
    const authError = Object.assign(new Error('auth required'), { status: 401 })
    vi.mocked(testConnector).mockRejectedValue(authError)

    fireEvent.click(await screen.findByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(testConnector).toHaveBeenCalled())
    expect(screen.queryByText(/Failed:/)).toBeNull()
  })

  it('shows the failure reason for a plain manage-page test error', async () => {
    renderTab(`/settings/connectors/${calendarConnector.id}`)
    vi.mocked(testConnector).mockRejectedValue(new Error('timeout'))

    fireEvent.click(await screen.findByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText(/Failed: timeout/)).toBeTruthy()
  })

  it('toasts when reconnecting oauth fails from the manage page', async () => {
    vi.mocked(connectorOAuthStart).mockRejectedValue(new Error('provider unreachable'))
    renderTab(`/settings/connectors/${calendarConnector.id}`)

    fireEvent.click(await screen.findByRole('button', { name: 'Reconnect Google account' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not start Google re-connect', {
        description: 'provider unreachable',
      }),
    )
  })
})

describe('ConnectorsList runTest auth-error handling', () => {
  it('does not surface a test result when testConnector throws a Timothy auth error', async () => {
    renderTab()
    await screen.findByText('calendar')
    const authError = Object.assign(new Error('auth required'), { status: 401 })
    vi.mocked(testConnector).mockRejectedValue(authError)

    fireEvent.click(screen.getByRole('button', { name: 'Test' }))
    await waitFor(() => expect(testConnector).toHaveBeenCalled())
    expect(screen.queryByText(/Failed:/)).toBeNull()
  })

  it('shows the failure reason for a plain test error from the list card', async () => {
    renderTab()
    await screen.findByText('calendar')
    vi.mocked(testConnector).mockRejectedValue(new Error('timeout'))

    fireEvent.click(screen.getByRole('button', { name: 'Test' }))
    expect(await screen.findByText(/Failed: timeout/)).toBeTruthy()
  })

  it('toasts when the list-card enabled toggle fails', async () => {
    vi.mocked(patchConnector).mockRejectedValue(new Error('locked'))
    renderTab()

    fireEvent.click(await screen.findByRole('switch', { name: 'google-calendar enabled' }))
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not update connector', { description: 'locked' }),
    )
  })

  it('toasts when loading the connectors list fails', async () => {
    vi.mocked(listConnectors).mockRejectedValue(new Error('network down'))
    renderTab()

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not load connectors', { description: 'network down' }),
    )
  })
})
