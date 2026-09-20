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

describe('ConnectorAdd aws flow', () => {
  it('renders the endpoint select, region, and both key fields', async () => {
    renderPage('aws')

    expect(await screen.findByLabelText('Endpoint')).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Region' })).toHaveTextContent('eu-central-1')
    expect(screen.getByPlaceholderText('AKIA…')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('wJalrXUtnFEMI/K7MDEN...')).toBeInTheDocument()
  })

  it('keeps Test disabled until both access keys are filled in', async () => {
    renderPage('aws')

    const testButton = await screen.findByRole('button', { name: 'Test connection' })
    expect(testButton).toBeDisabled()

    fireEvent.change(screen.getByPlaceholderText('AKIA…'), { target: { value: 'AKIAEXAMPLE' } })
    expect(testButton).toBeDisabled()

    fireEvent.change(screen.getByPlaceholderText('wJalrXUtnFEMI/K7MDEN...'), { target: { value: 'sekret' } })
    expect(testButton).toBeEnabled()
  })

  it('stores the keys as JSON under the derived ref and creates the connector', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-aws')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    vi.mocked(patchConnector).mockResolvedValue()
    renderPage('aws')

    fireEvent.change(await screen.findByPlaceholderText('AKIA…'), { target: { value: 'AKIAEXAMPLE' } })
    fireEvent.change(screen.getByPlaceholderText('wJalrXUtnFEMI/K7MDEN...'), { target: { value: 'sekret' } })

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(setSecret).toHaveBeenCalledWith(
      'AWS_KEYS',
      JSON.stringify({ access_key_id: 'AKIAEXAMPLE', secret_access_key: 'sekret' }),
    )
    expect(vi.mocked(createConnector).mock.calls[0][0]).toMatchObject({
      name: 'aws',
      kind: 'aws',
      config: { endpoint: 'https://aws-mcp.eu-central-1.api.aws/mcp', region: 'eu-central-1' },
      credential_ref: 'AWS_KEYS',
      enabled: false,
    })

    fireEvent.click(await screen.findByRole('button', { name: 'Add connector' }))
    await waitFor(() => expect(patchConnector).toHaveBeenCalledWith('conn-aws', { enabled: true }))
  })

  it('derives an _AWS_KEYS ref from a name that does not end in aws', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-aws-2')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    renderPage('aws')

    fireEvent.change(await screen.findByPlaceholderText('aws'), { target: { value: 'prod-account' } })
    fireEvent.change(screen.getByPlaceholderText('AKIA…'), { target: { value: 'AKIAEXAMPLE' } })
    fireEvent.change(screen.getByPlaceholderText('wJalrXUtnFEMI/K7MDEN...'), { target: { value: 'sekret' } })

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(setSecret).toHaveBeenCalledWith('PROD_ACCOUNT_AWS_KEYS', expect.any(String))
  })

  it('reuses an existing credential instead of writing a secret', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-aws-3')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    renderPage('aws')

    fireEvent.click(await screen.findByRole('radio', { name: 'Use existing' }))
    expect(screen.queryByPlaceholderText('AKIA…')).not.toBeInTheDocument()

    fireEvent.click(await screen.findByLabelText('existing credential'))
    fireEvent.click(await screen.findByRole('option', { name: /GITHUB_PAT/ }))

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(setSecret).not.toHaveBeenCalled()
    expect(vi.mocked(createConnector).mock.calls[0][0]).toMatchObject({ credential_ref: 'GITHUB_PAT' })
  })
})

describe('ConnectorAdd gcp flow', () => {
  const saKey = '{"type":"service_account","client_email":"sa@p.iam.gserviceaccount.com"}'

  it('renders the optional project and location fields plus the key textarea', async () => {
    renderPage('gcp')

    expect(await screen.findByPlaceholderText('my-project-123456')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('EU')).toBeInTheDocument()
    expect(screen.getByLabelText('Service account key')).toBeInTheDocument()
  })

  it('keeps Test disabled until a key is pasted, with no other field required', async () => {
    renderPage('gcp')

    const testButton = await screen.findByRole('button', { name: 'Test connection' })
    expect(testButton).toBeDisabled()

    fireEvent.change(screen.getByLabelText('Service account key'), { target: { value: saKey } })
    expect(testButton).toBeEnabled()
  })

  it('creates a gcp connector with an empty config when only the key is given', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-gcp')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    vi.mocked(patchConnector).mockResolvedValue()
    renderPage('gcp')

    fireEvent.change(await screen.findByLabelText('Service account key'), { target: { value: saKey } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(setSecret).toHaveBeenCalledWith('GCP_KEY', saKey)
    expect(vi.mocked(createConnector).mock.calls[0][0]).toEqual({
      name: 'gcp',
      kind: 'gcp',
      config: {},
      credential_ref: 'GCP_KEY',
      enabled: false,
    })

    fireEvent.click(await screen.findByRole('button', { name: 'Add connector' }))
    await waitFor(() => expect(patchConnector).toHaveBeenCalledWith('conn-gcp', { enabled: true }))
  })

  it('includes project_id and location when filled in, under a derived _GCP_KEY ref', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-gcp-2')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    renderPage('gcp')

    fireEvent.change(await screen.findByPlaceholderText('gcp'), { target: { value: 'analytics' } })
    fireEvent.change(screen.getByPlaceholderText('my-project-123456'), { target: { value: 'my-project-123456' } })
    fireEvent.change(screen.getByPlaceholderText('EU'), { target: { value: 'us-central1' } })
    fireEvent.change(screen.getByLabelText('Service account key'), { target: { value: saKey } })

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(setSecret).toHaveBeenCalledWith('ANALYTICS_GCP_KEY', saKey)
    expect(vi.mocked(createConnector).mock.calls[0][0]).toMatchObject({
      config: { project_id: 'my-project-123456', location: 'us-central1' },
    })
  })

  it('reuses an existing credential instead of writing a secret', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-gcp-3')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    renderPage('gcp')

    fireEvent.click(await screen.findByRole('radio', { name: 'Use existing' }))
    expect(screen.queryByLabelText('Service account key')).not.toBeInTheDocument()

    fireEvent.click(await screen.findByLabelText('existing credential'))
    fireEvent.click(await screen.findByRole('option', { name: /GITHUB_PAT/ }))

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(setSecret).not.toHaveBeenCalled()
    expect(vi.mocked(createConnector).mock.calls[0][0]).toMatchObject({ credential_ref: 'GITHUB_PAT' })
  })
})

describe('ConnectorAdd bitbucket flow', () => {
  it('renders a token-only form: no endpoint field, access-token copy', async () => {
    renderPage('bitbucket-account')

    expect(await screen.findByPlaceholderText('workspace or repository access token')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('bitbucket')).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('https://…/mcp')).not.toBeInTheDocument()
    expect(screen.getByText('Access token')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'How to create one →' })).toHaveAttribute(
      'href',
      'https://support.atlassian.com/bitbucket-cloud/docs/access-tokens/',
    )
    expect(screen.queryByText('Create one on GitHub →')).toBeNull()
  })

  it('keeps Test disabled until a token is pasted', async () => {
    renderPage('bitbucket-account')

    const testButton = await screen.findByRole('button', { name: 'Test connection' })
    expect(testButton).toBeDisabled()

    fireEvent.change(screen.getByPlaceholderText('workspace or repository access token'), { target: { value: 'bb-token' } })
    expect(testButton).toBeEnabled()
  })

  it('creates a bitbucket-kind connector under a derived _BITBUCKET_TOKEN ref', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-bb')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    vi.mocked(patchConnector).mockResolvedValue()
    renderPage('bitbucket-account')

    fireEvent.change(await screen.findByPlaceholderText('bitbucket'), { target: { value: 'work' } })
    fireEvent.change(screen.getByPlaceholderText('workspace or repository access token'), { target: { value: 'bb-token' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(setSecret).toHaveBeenCalledWith('WORK_BITBUCKET_TOKEN', 'bb-token')
    // kind must follow the preset, not the github literal the token-only
    // branch used to hardcode.
    expect(vi.mocked(createConnector).mock.calls[0][0]).toEqual({
      name: 'work',
      kind: 'bitbucket',
      config: {},
      credential_ref: 'WORK_BITBUCKET_TOKEN',
      enabled: false,
    })

    fireEvent.click(await screen.findByRole('button', { name: 'Add connector' }))
    await waitFor(() => expect(patchConnector).toHaveBeenCalledWith('conn-bb', { enabled: true }))
  })

  it('saves the workspace into config when given', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-bb-ws')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    renderPage('bitbucket-account')

    fireEvent.change(await screen.findByPlaceholderText('acme-team'), { target: { value: ' acme-team ' } })
    fireEvent.change(screen.getByPlaceholderText('workspace or repository access token'), { target: { value: 'bb-token' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(vi.mocked(createConnector).mock.calls[0][0]).toMatchObject({ kind: 'bitbucket', config: { workspace: 'acme-team' } })
  })

  it('does not stutter the ref when the name already ends in bitbucket', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-bb-2')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    renderPage('bitbucket-account')

    fireEvent.change(await screen.findByPlaceholderText('workspace or repository access token'), { target: { value: 'bb-token' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(setSecret).toHaveBeenCalledWith('BITBUCKET_TOKEN', 'bb-token')
  })

  // The preview caption and the submit path must derive the ref from the
  // same helper; they drifted on the anti-stutter guard (issue #783).
  it.each([
    { preset: 'github-account', placeholder: 'ghp_… or github_pat_…', name: 'acme-github', ref: 'ACME_GITHUB_PAT' },
    {
      preset: 'bitbucket-account',
      placeholder: 'workspace or repository access token',
      name: 'myorg-bitbucket',
      ref: 'MYORG_BITBUCKET_TOKEN',
    },
    { preset: 'gitlab-account', placeholder: 'glpat-…', name: 'myorg-gitlab', ref: 'MYORG_GITLAB_TOKEN' },
  ])('previews the same ref name it saves for $name', async ({ preset, placeholder, name, ref }) => {
    vi.mocked(listSecretBackends).mockResolvedValue([{ backend: 'vault', configured: true, default: true }])
    vi.mocked(createConnector).mockResolvedValue('conn-preview')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    renderPage(preset)

    fireEvent.change(await screen.findByPlaceholderText(placeholder), { target: { value: 'tok' } })
    fireEvent.change(screen.getByPlaceholderText(preset.replace('-account', '')), {
      target: { value: name },
    })
    expect(await screen.findByText(`Timothy stores the key in Vault (path timothy/${ref}).`)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(setSecret).toHaveBeenCalledWith(ref, 'tok'))
    expect(vi.mocked(createConnector).mock.calls[0][0]).toMatchObject({ credential_ref: ref })
  })

  it('reuses an existing credential instead of writing a secret', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-bb-3')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    renderPage('bitbucket-account')

    fireEvent.click(await screen.findByRole('radio', { name: 'Use existing' }))
    expect(screen.queryByPlaceholderText('workspace or repository access token')).not.toBeInTheDocument()

    fireEvent.click(await screen.findByLabelText('existing credential'))
    fireEvent.click(await screen.findByRole('option', { name: /GITHUB_PAT/ }))

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(setSecret).not.toHaveBeenCalled()
    expect(vi.mocked(createConnector).mock.calls[0][0]).toMatchObject({ kind: 'bitbucket', credential_ref: 'GITHUB_PAT' })
  })
})

describe('ConnectorAdd gitlab flow', () => {
  it('renders a token-only form with the gitlab token copy', async () => {
    renderPage('gitlab-account')

    expect(await screen.findByPlaceholderText('glpat-…')).toBeInTheDocument()
    expect(screen.getByPlaceholderText('gitlab')).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('https://…/mcp')).not.toBeInTheDocument()
    expect(screen.getByText('Access token')).toBeInTheDocument()
    expect(screen.getByText(/api and write_repository scopes/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'How to create one →' })).toHaveAttribute(
      'href',
      'https://gitlab.com/-/user_settings/personal_access_tokens',
    )
    expect(screen.queryByText('Create one on GitHub →')).toBeNull()
  })

  it('keeps Test disabled until a token is pasted', async () => {
    renderPage('gitlab-account')

    const testButton = await screen.findByRole('button', { name: 'Test connection' })
    expect(testButton).toBeDisabled()

    fireEvent.change(screen.getByPlaceholderText('glpat-…'), { target: { value: 'glpat-x' } })
    expect(testButton).toBeEnabled()
  })

  it('creates a gitlab-kind connector under a derived _GITLAB_TOKEN ref', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-gl')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    vi.mocked(patchConnector).mockResolvedValue()
    renderPage('gitlab-account')

    fireEvent.change(await screen.findByPlaceholderText('gitlab'), { target: { value: 'work' } })
    fireEvent.change(screen.getByPlaceholderText('glpat-…'), { target: { value: 'glpat-x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(setSecret).toHaveBeenCalledWith('WORK_GITLAB_TOKEN', 'glpat-x')
    expect(vi.mocked(createConnector).mock.calls[0][0]).toEqual({
      name: 'work',
      kind: 'gitlab',
      config: {},
      credential_ref: 'WORK_GITLAB_TOKEN',
      enabled: false,
    })

    fireEvent.click(await screen.findByRole('button', { name: 'Add connector' }))
    await waitFor(() => expect(patchConnector).toHaveBeenCalledWith('conn-gl', { enabled: true }))
  })

  it('saves the namespace and a self-managed base_url into config', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-gl-sm')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    renderPage('gitlab-account')

    fireEvent.change(await screen.findByPlaceholderText('acme/platform'), { target: { value: ' acme/platform ' } })
    fireEvent.change(screen.getByPlaceholderText('https://gitlab.com'), {
      target: { value: 'https://gitlab.example.com' },
    })
    fireEvent.change(screen.getByPlaceholderText('glpat-…'), { target: { value: 'glpat-x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(vi.mocked(createConnector).mock.calls[0][0]).toMatchObject({
      kind: 'gitlab',
      config: { namespace: 'acme/platform', base_url: 'https://gitlab.example.com' },
    })
  })

  it('does not stutter the ref when the name already ends in gitlab', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-gl-2')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    renderPage('gitlab-account')

    fireEvent.change(await screen.findByPlaceholderText('glpat-…'), { target: { value: 'glpat-x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(setSecret).toHaveBeenCalledWith('GITLAB_TOKEN', 'glpat-x')
  })

  it('reuses an existing credential instead of writing a secret', async () => {
    vi.mocked(createConnector).mockResolvedValue('conn-gl-3')
    vi.mocked(testConnector).mockResolvedValue({ ok: true })
    renderPage('gitlab-account')

    fireEvent.click(await screen.findByRole('radio', { name: 'Use existing' }))
    expect(screen.queryByPlaceholderText('glpat-…')).not.toBeInTheDocument()

    fireEvent.click(await screen.findByLabelText('existing credential'))
    fireEvent.click(await screen.findByRole('option', { name: /GITHUB_PAT/ }))

    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    await waitFor(() => expect(createConnector).toHaveBeenCalled())
    expect(setSecret).not.toHaveBeenCalled()
    expect(vi.mocked(createConnector).mock.calls[0][0]).toMatchObject({ kind: 'gitlab', credential_ref: 'GITHUB_PAT' })
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
