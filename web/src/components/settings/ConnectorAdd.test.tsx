import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
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

import {
  createConnector,
  listSecretBackends,
  listSecretRefs,
  patchConnector,
  setSecret,
  testConnector,
} from '../../api/client'

function renderPage(presetId: string) {
  return render(
    <MemoryRouter initialEntries={[`/settings/connectors/new/${presetId}`]}>
      <Routes>
        <Route path="/settings/connectors/new/:presetId" element={<ConnectorAdd />} />
      </Routes>
    </MemoryRouter>,
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
    fireEvent.click(screen.getByRole('button', { name: 'Use existing' }))
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
    fireEvent.click(screen.getByRole('button', { name: 'Use existing' }))

    fireEvent.click(await screen.findByLabelText('existing credential'))
    const option = await screen.findByRole('option', { name: /GMAIL_GOOGLE_OAUTH.*OAuth tokens \(managed by connector\)/ })
    expect(option).toHaveAttribute('aria-disabled', 'true')
  })
})
