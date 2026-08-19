import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SecretRefEntry } from '../../api/client'
import { CredentialsTab } from './CredentialsTab'

vi.mock('../../api/client', () => ({
  deleteSecret: vi.fn(),
  listSecretRefs: vi.fn(),
}))

import { deleteSecret, listSecretRefs } from '../../api/client'

const referenced: SecretRefEntry = {
  name: 'GITHUB_PAT',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-02T00:00:00Z',
  referenced_by: [
    { kind: 'connector', name: 'github-mcp', role: 'credential' },
    { kind: 'provider', name: 'github-provider', role: 'credential' },
  ],
}

const orphaned: SecretRefEntry = {
  name: 'OLD_KEY',
  referenced_by: [],
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
})

describe('CredentialsTab', () => {
  it('renders every stored ref with its used-by chips and no delete button when referenced', async () => {
    vi.mocked(listSecretRefs).mockResolvedValue([referenced])
    render(<CredentialsTab />)

    expect(await screen.findByText('GITHUB_PAT')).toBeInTheDocument()
    expect(screen.getByText(/connector: github-mcp/)).toBeInTheDocument()
    expect(screen.getByText(/provider: github-provider/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Delete GITHUB_PAT' })).not.toBeInTheDocument()
  })

  it('shows an empty state when nothing is stored', async () => {
    vi.mocked(listSecretRefs).mockResolvedValue([])
    render(<CredentialsTab />)

    expect(await screen.findByText('No credentials stored yet.')).toBeInTheDocument()
  })

  it('marks an unreferenced ref as orphaned and offers a delete button', async () => {
    vi.mocked(listSecretRefs).mockResolvedValue([orphaned])
    render(<CredentialsTab />)

    expect(await screen.findByText('OLD_KEY')).toBeInTheDocument()
    expect(screen.getByText('orphaned')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Delete OLD_KEY' })).toBeInTheDocument()
  })

  it('requires confirmation before deleting, and refreshes the list after', async () => {
    vi.mocked(listSecretRefs).mockResolvedValueOnce([orphaned]).mockResolvedValueOnce([])
    vi.mocked(deleteSecret).mockResolvedValue()
    render(<CredentialsTab />)

    fireEvent.click(await screen.findByRole('button', { name: 'Delete OLD_KEY' }))
    expect(await screen.findByText('Delete OLD_KEY?')).toBeInTheDocument()
    expect(screen.getByText(/This cannot be undone/)).toBeInTheDocument()
    expect(deleteSecret).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(deleteSecret).toHaveBeenCalledWith('OLD_KEY'))
    await waitFor(() => expect(screen.getByText('No credentials stored yet.')).toBeInTheDocument())
  })

  it('cancelling the confirm dialog never calls deleteSecret', async () => {
    vi.mocked(listSecretRefs).mockResolvedValue([orphaned])
    render(<CredentialsTab />)

    fireEvent.click(await screen.findByRole('button', { name: 'Delete OLD_KEY' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }))

    expect(deleteSecret).not.toHaveBeenCalled()
  })
})
