import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SecretRefEntry } from '../../api/client'
import { CredentialsTab } from './CredentialsTab'

vi.mock('../../api/client', () => ({
  deleteSecret: vi.fn(),
  listSecretRefs: vi.fn(),
  migrateAllSecrets: vi.fn(),
}))
vi.mock('./useDefaultSecretBackend', () => ({
  useDefaultSecretBackend: vi.fn(() => 'db'),
}))

import { deleteSecret, listSecretRefs, migrateAllSecrets } from '../../api/client'
import { useDefaultSecretBackend } from './useDefaultSecretBackend'

const referenced: SecretRefEntry = {
  name: 'GITHUB_PAT',
  backend: 'db',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-02T00:00:00Z',
  referenced_by: [
    { kind: 'connector', name: 'github-mcp', role: 'credential' },
    { kind: 'provider', name: 'github-provider', role: 'credential' },
  ],
  system: false,
}

const orphaned: SecretRefEntry = {
  name: 'OLD_KEY',
  backend: 'db',
  referenced_by: [],
  system: false,
}

const systemRef: SecretRefEntry = {
  name: 'VAULT_TOKEN',
  backend: 'db',
  referenced_by: [],
  system: true,
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(useDefaultSecretBackend).mockReturnValue('db')
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

  it('shows a System badge and disables delete for a bootstrap-credential ref', async () => {
    vi.mocked(listSecretRefs).mockResolvedValue([systemRef])
    render(<CredentialsTab />)

    expect(await screen.findByText('VAULT_TOKEN')).toBeInTheDocument()
    expect(screen.getByText('System')).toBeInTheDocument()
    expect(screen.queryByText('orphaned')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Delete VAULT_TOKEN' })).not.toBeInTheDocument()
  })

  it('hides the migrate-all button when the default backend is db', async () => {
    vi.mocked(useDefaultSecretBackend).mockReturnValue('db')
    vi.mocked(listSecretRefs).mockResolvedValue([{ ...orphaned, backend: 'vault' }])
    render(<CredentialsTab />)

    await screen.findByText('OLD_KEY')
    expect(screen.queryByRole('button', { name: /Migrate all to/ })).not.toBeInTheDocument()
  })

  it('hides the migrate-all button when every ref is already on the default backend', async () => {
    vi.mocked(useDefaultSecretBackend).mockReturnValue('vault')
    vi.mocked(listSecretRefs).mockResolvedValue([{ ...orphaned, backend: 'vault' }])
    render(<CredentialsTab />)

    await screen.findByText('OLD_KEY')
    expect(screen.queryByRole('button', { name: /Migrate all to/ })).not.toBeInTheDocument()
  })

  it('hides the migrate-all banner when only a system ref lives off the default backend', async () => {
    vi.mocked(useDefaultSecretBackend).mockReturnValue('vault')
    vi.mocked(listSecretRefs).mockResolvedValue([systemRef, { ...orphaned, backend: 'vault' }])
    render(<CredentialsTab />)

    await screen.findByText('VAULT_TOKEN')
    expect(screen.queryByRole('button', { name: /Migrate all to/ })).not.toBeInTheDocument()
  })

  it('shows migrate-all when the default backend is external and a ref lives elsewhere', async () => {
    vi.mocked(useDefaultSecretBackend).mockReturnValue('vault')
    vi.mocked(listSecretRefs).mockResolvedValue([{ ...orphaned, backend: 'db' }])
    vi.mocked(migrateAllSecrets).mockResolvedValue([{ name: 'OLD_KEY', migrated: true, skipped: false }])
    render(<CredentialsTab />)

    const button = await screen.findByRole('button', { name: 'Migrate all to Vault' })
    fireEvent.click(button)

    await waitFor(() => expect(migrateAllSecrets).toHaveBeenCalledWith('vault'))
  })
})
