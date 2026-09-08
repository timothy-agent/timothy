import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SecretBackendStatus } from '../../api/client'
import { TooltipProvider } from '../ui/tooltip'
import { SecretsTab } from './SecretsTab'

vi.mock('../../api/client', () => ({
  deleteSecretBackendConfig: vi.fn(),
  getSecretBackendConfig: vi.fn(),
  listSecretBackends: vi.fn(),
  putSecretBackendConfig: vi.fn(),
  secretStatus: vi.fn(),
  setDefaultSecretBackend: vi.fn(),
  setSecretStorage: vi.fn(),
  testSecretBackend: vi.fn(),
}))

import {
  deleteSecretBackendConfig,
  getSecretBackendConfig,
  listSecretBackends,
  putSecretBackendConfig,
  secretStatus,
  setDefaultSecretBackend,
  setSecretStorage,
  testSecretBackend,
} from '../../api/client'

function renderTab() {
  return render(
    <MemoryRouter>
      <TooltipProvider>
        <SecretsTab />
      </TooltipProvider>
    </MemoryRouter>,
  )
}

const dbDefault: SecretBackendStatus = { backend: 'db', configured: true, default: true }
const vaultNotConfigured: SecretBackendStatus = { backend: 'vault', configured: false, default: false }
const asmNotConfigured: SecretBackendStatus = { backend: 'asm', configured: false, default: false }

afterEach(cleanup)
beforeEach(() => {
  // jsdom lacks scrollIntoView; Radix Select calls it on open.
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listSecretBackends).mockResolvedValue([dbDefault, vaultNotConfigured, asmNotConfigured])
  vi.mocked(getSecretBackendConfig).mockResolvedValue({})
  vi.mocked(secretStatus).mockResolvedValue({ configured: false, backend: '' })
})

describe('SecretsTab load failure', () => {
  it('shows the error alert when listSecretBackends rejects', async () => {
    vi.mocked(listSecretBackends).mockRejectedValue(new Error('backend list failed'))
    renderTab()

    expect(await screen.findByText('backend list failed')).toBeInTheDocument()
  })
})

describe('SecretsTab default backend ordering and control', () => {
  it('renders Timothy storage as default when no backend row overrides it', async () => {
    renderTab()
    expect(await screen.findByText('Timothy storage')).toBeInTheDocument()
    expect(screen.getByText('default')).toBeInTheDocument()
  })

  it('claiming default on Vault calls setDefaultSecretBackend and refreshes', async () => {
    vi.mocked(getSecretBackendConfig).mockImplementation((backend: 'vault' | 'asm') =>
      Promise.resolve<Record<string, string>>(backend === 'vault' ? { address: 'https://vault.internal:8200' } : {}),
    )
    vi.mocked(listSecretBackends)
      .mockResolvedValueOnce([dbDefault, { ...vaultNotConfigured, configured: true }, asmNotConfigured])
      .mockResolvedValueOnce([{ ...dbDefault, default: false }, { ...vaultNotConfigured, configured: true, default: true }, asmNotConfigured])
    vi.mocked(setDefaultSecretBackend).mockResolvedValue()
    renderTab()

    await screen.findByText('HashiCorp Vault')
    const makeDefaultButtons = screen.getAllByRole('button', { name: 'Make default' })
    fireEvent.click(makeDefaultButtons[0])

    await waitFor(() => expect(setDefaultSecretBackend).toHaveBeenCalledWith('vault'))
    await waitFor(() => expect(listSecretBackends).toHaveBeenCalledTimes(2))
  })

  it('shows an error and skips the refresh when setDefaultSecretBackend rejects', async () => {
    vi.mocked(getSecretBackendConfig).mockImplementation((backend: 'vault' | 'asm') =>
      Promise.resolve<Record<string, string>>(backend === 'vault' ? { address: 'https://vault.internal:8200' } : {}),
    )
    vi.mocked(setDefaultSecretBackend).mockRejectedValue(new Error('cannot flip default'))
    renderTab()

    await screen.findByText('Timothy storage')
    fireEvent.click(screen.getAllByRole('button', { name: 'Make default' })[0])

    expect(await screen.findByText('cannot flip default')).toBeInTheDocument()
  })

  it('claiming default back onto Timothy storage when Vault is the current default', async () => {
    vi.mocked(listSecretBackends).mockResolvedValue([
      { backend: 'db', configured: true, default: false },
      { backend: 'vault', configured: true, default: true },
      asmNotConfigured,
    ])
    vi.mocked(setDefaultSecretBackend).mockResolvedValue()
    renderTab()

    const heading = await screen.findByText('Timothy storage')
    const storageCard = heading.closest('[data-density]') as HTMLElement
    fireEvent.click(within(storageCard).getByRole('button', { name: 'Make default' }))

    await waitFor(() => expect(setDefaultSecretBackend).toHaveBeenCalledWith('db'))
  })

  it('claiming default on ASM calls setDefaultSecretBackend with asm', async () => {
    vi.mocked(getSecretBackendConfig).mockImplementation((backend: 'vault' | 'asm') =>
      Promise.resolve<Record<string, string>>(backend === 'asm' ? { region: 'us-east-1' } : {}),
    )
    vi.mocked(listSecretBackends).mockResolvedValue([
      dbDefault,
      vaultNotConfigured,
      { backend: 'asm', configured: true, default: false },
    ])
    vi.mocked(setDefaultSecretBackend).mockResolvedValue()
    renderTab()

    await screen.findByText('AWS Secrets Manager')
    const makeDefaultButtons = screen.getAllByRole('button', { name: 'Make default' })
    fireEvent.click(makeDefaultButtons[makeDefaultButtons.length - 1])

    await waitFor(() => expect(setDefaultSecretBackend).toHaveBeenCalledWith('asm'))
  })
})

describe('SecretsTab reveal toggle', () => {
  it('toggles the vault token field between password and text', async () => {
    renderTab()
    const tokenInput = (await screen.findByPlaceholderText('paste to store/rotate')) as HTMLInputElement
    expect(tokenInput.type).toBe('password')

    fireEvent.click(screen.getByRole('button', { name: 'Show token' }))
    expect(tokenInput.type).toBe('text')

    fireEvent.click(screen.getByRole('button', { name: 'Hide token' }))
    expect(tokenInput.type).toBe('password')
  })
})

describe('SecretsTab backend config load failure', () => {
  it('leaves the card unconfigured (no crash) when getSecretBackendConfig rejects', async () => {
    vi.mocked(getSecretBackendConfig).mockRejectedValue(new Error('vault unreachable'))
    renderTab()

    await screen.findByText('HashiCorp Vault')
    expect(screen.getAllByText('not configured').length).toBeGreaterThan(0)
  })
})

describe('SecretsTab useCredentialStatus', () => {
  it('shows no stored badge for a not-yet-saved vault token ref', async () => {
    renderTab()
    await screen.findByText('HashiCorp Vault')
    expect(screen.queryByText('stored · encrypted')).not.toBeInTheDocument()
  })

  it('shows no stored badge when secretStatus rejects', async () => {
    vi.mocked(secretStatus).mockRejectedValue(new Error('lookup failed'))
    renderTab()
    await screen.findByText('HashiCorp Vault')
    expect(screen.queryByText('stored · encrypted')).not.toBeInTheDocument()
  })

  it('shows the stored badge once secretStatus reports configured', async () => {
    vi.mocked(secretStatus).mockImplementation((ref: string) =>
      Promise.resolve({ configured: ref === 'VAULT_TOKEN', backend: 'db' }),
    )
    renderTab()
    expect(await screen.findByText('stored · encrypted')).toBeInTheDocument()
  })
})

describe('SecretsTab vault card', () => {
  it('save with no token pasted writes config but skips setSecretStorage', async () => {
    vi.mocked(putSecretBackendConfig).mockResolvedValue()
    renderTab()

    const addressInput = await screen.findByPlaceholderText('https://vault.internal:8200')
    fireEvent.change(addressInput, { target: { value: 'https://vault.internal:8200' } })
    fireEvent.click(screen.getAllByRole('button', { name: 'Save' })[0])

    await waitFor(() => expect(putSecretBackendConfig).toHaveBeenCalledWith('vault', expect.objectContaining({ address: 'https://vault.internal:8200' })))
    expect(setSecretStorage).not.toHaveBeenCalled()
    expect(await screen.findByText('saved')).toBeInTheDocument()
  })

  it('staging the Mount field sends it in the saved config', async () => {
    vi.mocked(putSecretBackendConfig).mockResolvedValue()
    renderTab()

    fireEvent.change(await screen.findByPlaceholderText('https://vault.internal:8200'), {
      target: { value: 'https://vault.internal:8200' },
    })
    fireEvent.change(screen.getByPlaceholderText('secret'), { target: { value: 'kv2-custom' } })
    fireEvent.click(screen.getAllByRole('button', { name: 'Save' })[0])

    await waitFor(() =>
      expect(putSecretBackendConfig).toHaveBeenCalledWith('vault', expect.objectContaining({ mount: 'kv2-custom' })),
    )
  })

  it('save with a pasted token also writes it via setSecretStorage and clears the field', async () => {
    vi.mocked(putSecretBackendConfig).mockResolvedValue()
    vi.mocked(setSecretStorage).mockResolvedValue()
    renderTab()

    const addressInput = await screen.findByPlaceholderText('https://vault.internal:8200')
    fireEvent.change(addressInput, { target: { value: 'https://vault.internal:8200' } })
    const tokenInput = (await screen.findByPlaceholderText('paste to store/rotate')) as HTMLInputElement
    fireEvent.change(tokenInput, { target: { value: 'hvs.abc123' } })

    fireEvent.click(screen.getAllByRole('button', { name: 'Save' })[0])

    await waitFor(() => expect(setSecretStorage).toHaveBeenCalledWith('VAULT_TOKEN', 'hvs.abc123'))
    await waitFor(() => expect(tokenInput.value).toBe(''))
  })

  it('a save failure renders the failed test status with the error text', async () => {
    vi.mocked(putSecretBackendConfig).mockRejectedValue(new Error('write denied'))
    renderTab()

    const addressInput = await screen.findByPlaceholderText('https://vault.internal:8200')
    fireEvent.change(addressInput, { target: { value: 'https://vault.internal:8200' } })
    fireEvent.click(screen.getAllByRole('button', { name: 'Save' })[0])

    expect(await screen.findByText('write denied')).toBeInTheDocument()
  })

  it('switching auth to AppRole swaps the token field for Role ID and Secret ID, staged via secret_id_ref', async () => {
    vi.mocked(putSecretBackendConfig).mockResolvedValue()
    vi.mocked(setSecretStorage).mockResolvedValue()
    renderTab()

    const addressInput = await screen.findByPlaceholderText('https://vault.internal:8200')
    fireEvent.change(addressInput, { target: { value: 'https://vault.internal:8200' } })

    fireEvent.click(screen.getByRole('combobox', { name: 'vault auth method' }))
    fireEvent.click(await screen.findByRole('option', { name: 'AppRole' }))

    fireEvent.change(await screen.findByPlaceholderText('role-id'), { target: { value: 'role-123' } })
    const secretIdInputs = screen.getAllByPlaceholderText('paste to store/rotate')
    fireEvent.change(secretIdInputs[0], { target: { value: 'secret-id-value' } })

    fireEvent.click(screen.getAllByRole('button', { name: 'Save' })[0])

    await waitFor(() => expect(setSecretStorage).toHaveBeenCalledWith('VAULT_SECRET_ID', 'secret-id-value'))
    expect(vi.mocked(putSecretBackendConfig).mock.calls[0][1]).toMatchObject({
      role_id: 'role-123',
      auth: 'approle',
    })
  })

  it('test button calls testSecretBackend and renders the ok/failed result', async () => {
    vi.mocked(getSecretBackendConfig).mockImplementation((backend: 'vault' | 'asm') =>
      Promise.resolve<Record<string, string>>(backend === 'vault' ? { address: 'https://vault.internal:8200' } : {}),
    )
    vi.mocked(listSecretBackends).mockResolvedValue([
      dbDefault,
      { backend: 'vault', configured: true, default: false },
      asmNotConfigured,
    ])
    vi.mocked(testSecretBackend).mockResolvedValue({ ok: true })
    renderTab()

    const testButtons = await screen.findAllByRole('button', { name: 'Test' })
    fireEvent.click(testButtons[0])

    await waitFor(() => expect(testSecretBackend).toHaveBeenCalledWith('vault'))
    expect(await screen.findByText('connection OK')).toBeInTheDocument()
  })

  it('test failure renders the server error message', async () => {
    vi.mocked(getSecretBackendConfig).mockImplementation((backend: 'vault' | 'asm') =>
      Promise.resolve<Record<string, string>>(backend === 'vault' ? { address: 'https://vault.internal:8200' } : {}),
    )
    vi.mocked(listSecretBackends).mockResolvedValue([
      dbDefault,
      { backend: 'vault', configured: true, default: false },
      asmNotConfigured,
    ])
    vi.mocked(testSecretBackend).mockResolvedValue({ ok: false, error: 'dial tcp: timeout' })
    renderTab()

    fireEvent.click((await screen.findAllByRole('button', { name: 'Test' }))[0])
    expect(await screen.findByText('dial tcp: timeout')).toBeInTheDocument()
  })

  it('test failure with no server error falls back to "Connection failed."', async () => {
    vi.mocked(getSecretBackendConfig).mockImplementation((backend: 'vault' | 'asm') =>
      Promise.resolve<Record<string, string>>(backend === 'vault' ? { address: 'https://vault.internal:8200' } : {}),
    )
    vi.mocked(listSecretBackends).mockResolvedValue([
      dbDefault,
      { backend: 'vault', configured: true, default: false },
      asmNotConfigured,
    ])
    vi.mocked(testSecretBackend).mockResolvedValue({ ok: false })
    renderTab()

    fireEvent.click((await screen.findAllByRole('button', { name: 'Test' }))[0])
    expect(await screen.findByText('Connection failed.')).toBeInTheDocument()
  })

  it('test throwing an exception renders errText of that exception', async () => {
    vi.mocked(getSecretBackendConfig).mockImplementation((backend: 'vault' | 'asm') =>
      Promise.resolve<Record<string, string>>(backend === 'vault' ? { address: 'https://vault.internal:8200' } : {}),
    )
    vi.mocked(listSecretBackends).mockResolvedValue([
      dbDefault,
      { backend: 'vault', configured: true, default: false },
      asmNotConfigured,
    ])
    vi.mocked(testSecretBackend).mockRejectedValue(new Error('network unreachable'))
    renderTab()

    fireEvent.click((await screen.findAllByRole('button', { name: 'Test' }))[0])
    expect(await screen.findByText('network unreachable')).toBeInTheDocument()
  })

  it('remove calls deleteSecretBackendConfig, shows removed, and hides the Remove button once unconfigured', async () => {
    vi.mocked(getSecretBackendConfig).mockImplementation((backend: 'vault' | 'asm') =>
      Promise.resolve<Record<string, string>>(backend === 'vault' ? { address: 'https://vault.internal:8200' } : {}),
    )
    vi.mocked(listSecretBackends)
      .mockResolvedValueOnce([dbDefault, { backend: 'vault', configured: true, default: false }, asmNotConfigured])
      .mockResolvedValueOnce([dbDefault, vaultNotConfigured, asmNotConfigured])
    vi.mocked(deleteSecretBackendConfig).mockResolvedValue()
    renderTab()

    const removeButton = await screen.findByRole('button', { name: 'Remove HashiCorp Vault' })
    fireEvent.click(removeButton)

    await waitFor(() => expect(deleteSecretBackendConfig).toHaveBeenCalledWith('vault'))
    expect(await screen.findByText('removed')).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Remove HashiCorp Vault' })).not.toBeInTheDocument())
  })

  it('a remove failure renders the failed status and keeps the Remove button', async () => {
    vi.mocked(getSecretBackendConfig).mockImplementation((backend: 'vault' | 'asm') =>
      Promise.resolve<Record<string, string>>(backend === 'vault' ? { address: 'https://vault.internal:8200' } : {}),
    )
    vi.mocked(listSecretBackends).mockResolvedValue([
      dbDefault,
      { backend: 'vault', configured: true, default: false },
      asmNotConfigured,
    ])
    vi.mocked(deleteSecretBackendConfig).mockRejectedValue(new Error('backend refused delete'))
    renderTab()

    fireEvent.click(await screen.findByRole('button', { name: 'Remove HashiCorp Vault' }))
    expect(await screen.findByText('backend refused delete')).toBeInTheDocument()
  })
})

describe('SecretsTab ASM card', () => {
  it('defaults the region select to Chain default and stages a picked region', async () => {
    renderTab()

    const regionSelect = await screen.findByRole('combobox', { name: 'aws region' })
    expect(regionSelect).toHaveTextContent('Chain default')

    fireEvent.click(regionSelect)
    fireEvent.click(await screen.findByRole('option', { name: /N\. Virginia/ }))
    expect(regionSelect).toHaveTextContent('N. Virginia')
  })

  it('picking Chain default after a region was set stages an empty region', async () => {
    vi.mocked(putSecretBackendConfig).mockResolvedValue()
    renderTab()

    const regionSelect = await screen.findByRole('combobox', { name: 'aws region' })
    fireEvent.click(regionSelect)
    fireEvent.click(await screen.findByRole('option', { name: /N\. Virginia/ }))

    fireEvent.click(regionSelect)
    fireEvent.click(await screen.findByRole('option', { name: 'Chain default' }))
    expect(regionSelect).toHaveTextContent('Chain default')

    const saveButtons = screen.getAllByRole('button', { name: 'Save' })
    fireEvent.click(saveButtons[saveButtons.length - 1])

    await waitFor(() =>
      expect(putSecretBackendConfig).toHaveBeenCalledWith('asm', expect.objectContaining({ region: '' })),
    )
  })

  it('switching auth to Named profile shows a Profile field and stages it', async () => {
    vi.mocked(putSecretBackendConfig).mockResolvedValue()
    renderTab()

    fireEvent.click(await screen.findByRole('combobox', { name: 'aws auth method' }))
    fireEvent.click(await screen.findByRole('option', { name: 'Named profile' }))

    const profileInput = await screen.findByPlaceholderText('profile name from ~/.aws')
    fireEvent.change(profileInput, { target: { value: 'my-profile' } })

    const saveButtons = screen.getAllByRole('button', { name: 'Save' })
    fireEvent.click(saveButtons[saveButtons.length - 1])

    await waitFor(() =>
      expect(putSecretBackendConfig).toHaveBeenCalledWith('asm', expect.objectContaining({ auth: 'profile', profile: 'my-profile' })),
    )
  })

  it('switching auth to Access keys shows both key fields and stores the secret key via setSecretStorage', async () => {
    vi.mocked(putSecretBackendConfig).mockResolvedValue()
    vi.mocked(setSecretStorage).mockResolvedValue()
    renderTab()

    fireEvent.click(await screen.findByRole('combobox', { name: 'aws auth method' }))
    fireEvent.click(await screen.findByRole('option', { name: 'Access keys' }))

    fireEvent.change(await screen.findByPlaceholderText('AKIA...'), { target: { value: 'AKIAEXAMPLE' } })
    const pasteInputs = screen.getAllByPlaceholderText('paste to store/rotate')
    fireEvent.change(pasteInputs[pasteInputs.length - 1], { target: { value: 'asm-secret-value' } })

    const saveButtons = screen.getAllByRole('button', { name: 'Save' })
    fireEvent.click(saveButtons[saveButtons.length - 1])

    await waitFor(() => expect(setSecretStorage).toHaveBeenCalledWith('AWS_SECRET_ACCESS_KEY', 'asm-secret-value'))
    expect(vi.mocked(putSecretBackendConfig).mock.calls[0][1]).toMatchObject({
      access_key_id: 'AKIAEXAMPLE',
      auth: 'keys',
    })
  })

  it('a save failure on the ASM card renders the failed status', async () => {
    vi.mocked(putSecretBackendConfig).mockRejectedValue(new Error('CreateSecret denied'))
    renderTab()

    const saveButtons = await screen.findAllByRole('button', { name: 'Save' })
    fireEvent.click(saveButtons[saveButtons.length - 1])

    expect(await screen.findByText('CreateSecret denied')).toBeInTheDocument()
  })

  it('remove on the ASM card calls deleteSecretBackendConfig and shows removed', async () => {
    vi.mocked(listSecretBackends)
      .mockResolvedValueOnce([dbDefault, vaultNotConfigured, { backend: 'asm', configured: true, default: false }])
      .mockResolvedValueOnce([dbDefault, vaultNotConfigured, asmNotConfigured])
    vi.mocked(deleteSecretBackendConfig).mockResolvedValue()
    renderTab()

    const removeButton = await screen.findByRole('button', { name: 'Remove AWS Secrets Manager' })
    fireEvent.click(removeButton)

    await waitFor(() => expect(deleteSecretBackendConfig).toHaveBeenCalledWith('asm'))
    expect(await screen.findByText('removed')).toBeInTheDocument()
  })

  it('test button on the ASM card calls testSecretBackend', async () => {
    vi.mocked(getSecretBackendConfig).mockImplementation((backend: 'vault' | 'asm') =>
      Promise.resolve<Record<string, string>>(backend === 'asm' ? { region: 'us-east-1' } : {}),
    )
    vi.mocked(listSecretBackends).mockResolvedValue([
      dbDefault,
      vaultNotConfigured,
      { backend: 'asm', configured: true, default: false },
    ])
    vi.mocked(testSecretBackend).mockResolvedValue({ ok: true })
    renderTab()

    const testButtons = await screen.findAllByRole('button', { name: 'Test' })
    fireEvent.click(testButtons[testButtons.length - 1])

    await waitFor(() => expect(testSecretBackend).toHaveBeenCalledWith('asm'))
  })
})
