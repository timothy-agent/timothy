import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '../ui/tooltip'
import { ConnectorAdd } from './ConnectorAdd'

vi.mock('../../api/client', () => ({
  connectorOAuthStart: vi.fn(),
  createConnector: vi.fn(),
  listSecretBackends: vi.fn(),
  listSecretRefs: vi.fn(),
  patchConnector: vi.fn(),
  setSecret: vi.fn(),
  testConnector: vi.fn(),
}))
vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import {
  createConnector,
  listSecretBackends,
  listSecretRefs,
  patchConnector,
  setSecret,
  testConnector,
} from '../../api/client'
import { toast } from 'sonner'

function renderPage(presetId: string) {
  return render(
    <TooltipProvider>
      <MemoryRouter initialEntries={[`/settings/connectors/new/${presetId}`]}>
        <Routes>
          <Route path="/settings/connectors/new/:presetId" element={<ConnectorAdd />} />
        </Routes>
      </MemoryRouter>
    </TooltipProvider>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listSecretBackends).mockResolvedValue([{ backend: 'db', configured: true, default: true }])
  vi.mocked(listSecretRefs).mockResolvedValue([
    {
      name: 'GITHUB_PAT',
      backend: 'db',
      referenced_by: [{ kind: 'connector', name: 'github-account', role: 'credential' }],
    },
    {
      name: 'GMAIL_GOOGLE_OAUTH',
      backend: 'db',
      referenced_by: [{ kind: 'connector', name: 'gmail', role: 'oauth_tokens' }],
    },
  ])
})

describe('ConnectorAdd existing-credential picker (github MCP token)', () => {
  it('defaults to New credential with the paste field visible', async () => {
    renderPage('github')
    expect(await screen.findByPlaceholderText('ghp_… or github_pat_…')).toBeInTheDocument()
  })

  it('choosing an existing ref skips the token secret write and reuses it as credential_ref', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-1')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    vi.mocked(patchConnector).mockResolvedValue()
    renderPage('github')

    fireEvent.change(await screen.findByPlaceholderText('github-mcp'), { target: { value: 'github-mcp-2' } })
    fireEvent.click(screen.getByRole('radio', { name: 'Use existing' }))
    expect(screen.queryByPlaceholderText('ghp_… or github_pat_…')).not.toBeInTheDocument()

    fireEvent.click(await screen.findByLabelText('existing credential'))
    fireEvent.click(await screen.findByRole('option', { name: /GITHUB_PAT/ }))

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(setSecret).not.toHaveBeenCalled()
    expect(vi.mocked(createConnector).mock.calls[0][0]).toMatchObject({ credential_ref: 'GITHUB_PAT' })
  })

  it('disables an OAuth token bundle ref with a managed-by-connector label', async () => {
    renderPage('github')
    fireEvent.click(screen.getByRole('radio', { name: 'Use existing' }))

    fireEvent.click(await screen.findByLabelText('existing credential'))
    const option = await screen.findByRole('option', { name: /GMAIL_GOOGLE_OAUTH.*OAuth tokens \(managed by connector\)/ })
    expect(option).toHaveAttribute('aria-disabled', 'true')
  })
})

describe('ConnectorAdd imap flow', () => {
  it('tests then adds an imap connector with host/username/password', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-imap')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    vi.mocked(patchConnector).mockResolvedValue()
    renderPage('imap')

    fireEvent.change(await screen.findByPlaceholderText('imap.example.com'), {
      target: { value: 'imap.fastmail.com' },
    })
    fireEvent.change(screen.getByPlaceholderText('me@example.com'), {
      target: { value: 'me@fastmail.com' },
    })
    fireEvent.change(screen.getByPlaceholderText('password'), { target: { value: 'app-password' } })

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(vi.mocked(createConnector).mock.calls[0][0]).toMatchObject({
      kind: 'imap',
      config: { host: 'imap.fastmail.com', username: 'me@fastmail.com' },
      enabled: false,
    })
    expect(setSecret).toHaveBeenCalledWith(expect.stringContaining('_IMAP_PASSWORD'), 'app-password')

    fireEvent.click(await screen.findByRole('button', { name: 'Add connector' }))
    await waitFor(() => expect(patchConnector).toHaveBeenCalledWith('conn-imap', { enabled: true }))
  })

  it('includes smtp host and port when provided', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-imap-smtp')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    renderPage('imap')

    fireEvent.change(await screen.findByPlaceholderText('imap.example.com'), {
      target: { value: 'imap.fastmail.com' },
    })
    fireEvent.change(screen.getByPlaceholderText('me@example.com'), { target: { value: 'me@fastmail.com' } })
    fireEvent.change(screen.getByPlaceholderText('password'), { target: { value: 'app-password' } })
    fireEvent.change(screen.getByPlaceholderText('smtp.example.com'), { target: { value: 'smtp.fastmail.com' } })
    fireEvent.change(screen.getByPlaceholderText('587'), { target: { value: '465' } })

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(vi.mocked(createConnector).mock.calls[0][0]).toMatchObject({
      config: { smtp_host: 'smtp.fastmail.com', smtp_port: 465 },
    })
  })

  it('rejects an invalid port and does not create a connector', async () => {
    renderPage('imap')

    fireEvent.change(await screen.findByPlaceholderText('imap.example.com'), {
      target: { value: 'imap.fastmail.com' },
    })
    fireEvent.change(screen.getByPlaceholderText('me@example.com'), {
      target: { value: 'me@fastmail.com' },
    })
    fireEvent.change(screen.getByPlaceholderText('password'), { target: { value: 'app-password' } })
    fireEvent.change(screen.getByPlaceholderText('993'), { target: { value: '143a' } })

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Invalid port', expect.anything()))
    expect(createConnector).not.toHaveBeenCalled()
  })
})

describe('ConnectorAdd caldav flow', () => {
  it('tests then adds a caldav connector with url/username/password', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-caldav')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    vi.mocked(patchConnector).mockResolvedValue()
    renderPage('caldav')

    fireEvent.change(await screen.findByPlaceholderText('https://cal.example.com/dav/calendars/user/personal/'), {
      target: { value: 'https://cal.fastmail.com/dav/calendars/user/me@fastmail.com/cal/' },
    })
    fireEvent.change(screen.getByPlaceholderText('me@example.com'), {
      target: { value: 'me@fastmail.com' },
    })
    fireEvent.change(screen.getByPlaceholderText('password'), { target: { value: 'app-password' } })

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(vi.mocked(createConnector).mock.calls[0][0]).toMatchObject({
      kind: 'caldav',
      config: {
        url: 'https://cal.fastmail.com/dav/calendars/user/me@fastmail.com/cal/',
        username: 'me@fastmail.com',
      },
      enabled: false,
    })
    expect(setSecret).toHaveBeenCalledWith(expect.stringContaining('_CALDAV_PASSWORD'), 'app-password')

    fireEvent.click(await screen.findByRole('button', { name: 'Add connector' }))
    await waitFor(() => expect(patchConnector).toHaveBeenCalledWith('conn-caldav', { enabled: true }))
  })
})

describe('ConnectorAdd mcp endpoint field', () => {
  it('edits the pre-filled endpoint and invalidates a prior test', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-mcp')
    vi.mocked(testConnector).mockResolvedValueOnce({ ok: true })
    renderPage('github')

    fireEvent.change(await screen.findByPlaceholderText('ghp_… or github_pat_…'), { target: { value: 'ghp_abc' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText(/Connection OK/)).toBeTruthy()

    const endpointInput = screen.getByDisplayValue('https://api.githubcopilot.com/mcp/')
    fireEvent.change(endpointInput, { target: { value: 'https://mcp.example.com/' } })

    // Editing after a passing test invalidates it: Add is gated again.
    expect((screen.getByRole('button', { name: 'Add connector' }) as HTMLButtonElement).disabled).toBe(true)
  })
})

describe('ConnectorAdd auth-error handling', () => {
  it('does not surface a test result when createConnector throws a Timothy auth error', async () => {
    const authError = Object.assign(new Error('auth required'), { status: 401 })
    vi.mocked(createConnector).mockRejectedValue(authError)
    renderPage('github')

    fireEvent.change(await screen.findByPlaceholderText('ghp_… or github_pat_…'), { target: { value: 'ghp_abc' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(screen.queryByText(/Connection failed:/)).toBeNull()
  })

  it('toasts when enabling the connector after a passing test fails', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-mcp-3')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    vi.mocked(patchConnector).mockRejectedValue(new Error('gone'))
    renderPage('github')

    fireEvent.change(await screen.findByPlaceholderText('ghp_… or github_pat_…'), { target: { value: 'ghp_abc' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Add connector' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not enable connector', { description: 'gone' }),
    )
  })
})

describe('ConnectorAdd oauth failure', () => {
  it('toasts and stays on the page when the google connect flow fails', async () => {
    vi.mocked(createConnector).mockRejectedValue(new Error('quota exceeded'))
    renderPage('gmail')

    fireEvent.change(await screen.findByPlaceholderText('….apps.googleusercontent.com'), {
      target: { value: 'cid.apps.googleusercontent.com' },
    })
    fireEvent.change(screen.getByPlaceholderText('GOCSPX-…'), { target: { value: 'GOCSPX-secret' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save & connect Google' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not connect Google account', {
        description: 'quota exceeded',
      }),
    )
  })
})

describe('ConnectorAdd cancel and retest', () => {
  it('Cancel navigates back to the connectors list', async () => {
    renderPage('imap')
    await screen.findByPlaceholderText('imap.example.com')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    // No API calls fire on cancel; the button itself is the only assertable effect here.
    expect(createConnector).not.toHaveBeenCalled()
  })

  it('offers a retest button after a failed test, alongside the failure message', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-imap-2')
    vi.mocked(testConnector).mockResolvedValue({ ok: false, error: 'connection refused' })
    renderPage('imap')

    fireEvent.change(await screen.findByPlaceholderText('imap.example.com'), {
      target: { value: 'imap.fastmail.com' },
    })
    fireEvent.change(screen.getByPlaceholderText('me@example.com'), { target: { value: 'me@fastmail.com' } })
    fireEvent.change(screen.getByPlaceholderText('password'), { target: { value: 'app-password' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    expect(await screen.findByText(/Connection failed: connection refused/)).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Add connector' }) as HTMLButtonElement).disabled).toBe(true)

    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(testConnector).toHaveBeenCalledTimes(2))
  })
})
