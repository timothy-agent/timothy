import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminConnector, Destination } from '../../api/types'
import { TooltipProvider } from '../ui/tooltip'
import { DestinationsTab } from './DestinationsTab'

vi.mock('../../api/client', () => ({
  createDestination: vi.fn(),
  deleteDestination: vi.fn(),
  listConnectors: vi.fn(),
  listDestinations: vi.fn(),
  listSecretBackends: vi.fn(),
  listSecretRefs: vi.fn(),
  patchDestination: vi.fn(),
  setSecret: vi.fn(),
  testDestination: vi.fn(),
}))

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import {
  createDestination,
  deleteDestination,
  listConnectors,
  listDestinations,
  listSecretBackends,
  listSecretRefs,
  patchDestination,
  setSecret,
  testDestination,
} from '../../api/client'
import { toast } from 'sonner'

const webhookDestination: Destination = {
  id: 'd1',
  name: 'ops-hook',
  kind: 'webhook',
  config: { url: 'https://example.com/hook', format: 'json' },
  credential_ref: '',
  enabled: true,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

const telegramDestination: Destination = {
  id: 'd4',
  name: 'ops-telegram',
  kind: 'telegram',
  config: { chat_id: '123456' },
  credential_ref: 'OPS_TELEGRAM_TELEGRAM_BOT_TOKEN',
  enabled: true,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

const googleConnector: AdminConnector = {
  id: 'c1',
  name: 'gmail',
  kind: 'google',
  config: { scopes: ['https://www.googleapis.com/auth/gmail.send'] },
  credential_ref: 'GMAIL_GOOGLE_OAUTH',
  enabled: true,
  sensitive: false,
}

const githubConnector: AdminConnector = {
  id: 'c2',
  name: 'my-github',
  kind: 'github',
  config: {},
  credential_ref: 'MY_GITHUB_PAT',
  enabled: true,
  sensitive: false,
}

const githubDestination: Destination = {
  id: 'd5',
  name: 'ops-repo',
  kind: 'github',
  config: { connector_id: 'c2', mode: 'push_pr' },
  credential_ref: '',
  enabled: true,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

function renderTab(entry = '/settings/destinations') {
  return render(
    <TooltipProvider>
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path="/settings/destinations/*" element={<DestinationsTab />} />
        </Routes>
      </MemoryRouter>
    </TooltipProvider>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listDestinations).mockResolvedValue([])
  vi.mocked(listConnectors).mockResolvedValue([googleConnector, githubConnector])
  vi.mocked(listSecretBackends).mockResolvedValue([{ backend: 'db', configured: true, default: true }])
  vi.mocked(listSecretRefs).mockResolvedValue([])
})

describe('Destinations tab', () => {
  it('shows the empty state and the add tiles when there are none', async () => {
    renderTab()
    expect(await screen.findByText('Your destinations')).toBeTruthy()
    expect(screen.getByText('No destinations yet')).toBeTruthy()
    expect(screen.getByText('Add one below.')).toBeTruthy()
    expect(screen.getByRole('link', { name: /^Email/ })).toBeTruthy()
    expect(screen.getByRole('link', { name: /^Webhook/ })).toBeTruthy()
  })

  it('renders a configured destination card', async () => {
    vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
    renderTab()
    expect(await screen.findByText('Your destinations · 1')).toBeTruthy()
    expect(screen.getByText('ops-hook')).toBeTruthy()
    expect(screen.getByText('https://example.com/hook')).toBeTruthy()
  })

  it('toggles a destination enabled from the list card', async () => {
    vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
    vi.mocked(patchDestination).mockResolvedValue()
    renderTab()

    const toggle = await screen.findByRole('switch', { name: 'ops-hook enabled' })
    fireEvent.click(toggle)

    await waitFor(() => expect(patchDestination).toHaveBeenCalledWith('d1', { enabled: false }))
  })

  it('test-sends from the list card and shows success', async () => {
    vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
    vi.mocked(testDestination).mockResolvedValue({ ok: true })
    renderTab()

    fireEvent.click(await screen.findByRole('button', { name: 'Test send' }))
    expect(await screen.findByText('Test delivery sent')).toBeTruthy()
    expect(testDestination).toHaveBeenCalledWith('d1')
  })

  it('test-sends from the list card and shows the failure reason', async () => {
    vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
    vi.mocked(testDestination).mockResolvedValue({ ok: false, error: 'connection refused' })
    renderTab()

    fireEvent.click(await screen.findByRole('button', { name: 'Test send' }))
    expect(await screen.findByText(/Failed: connection refused/)).toBeTruthy()
  })

  it('surfaces the 409 referenced error when delete is refused', async () => {
    vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
    vi.mocked(deleteDestination).mockRejectedValue(
      new Error('destination is referenced by an active mission'),
    )
    renderTab()

    // Opens the confirm dialog.
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }))
    // Scope to the dialog so the card's own Delete button isn't matched.
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        'Could not remove destination',
        expect.objectContaining({ description: expect.stringContaining('referenced by an active mission') }),
      ),
    )
  })

  it('toasts when loading destinations fails', async () => {
    vi.mocked(listDestinations).mockRejectedValue(new Error('network down'))
    renderTab()

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not load destinations', { description: 'network down' }),
    )
  })

  it('toasts when the list-card enabled toggle fails', async () => {
    vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
    vi.mocked(patchDestination).mockRejectedValue(new Error('locked'))
    renderTab()

    fireEvent.click(await screen.findByRole('switch', { name: 'ops-hook enabled' }))
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not update destination', { description: 'locked' }),
    )
  })

  it('shows the failure reason for a thrown (non-API-shaped) test error from the list card', async () => {
    vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
    vi.mocked(testDestination).mockRejectedValue(new Error('timeout'))
    renderTab()

    fireEvent.click(await screen.findByRole('button', { name: 'Test send' }))
    expect(await screen.findByText(/Failed: timeout/)).toBeTruthy()
  })

  it('deletes a destination and refreshes the list', async () => {
    vi.mocked(listDestinations).mockResolvedValueOnce([webhookDestination]).mockResolvedValueOnce([])
    vi.mocked(deleteDestination).mockResolvedValue()
    renderTab()

    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }))
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(deleteDestination).toHaveBeenCalledWith('d1'))
    expect(await screen.findByText('No destinations yet')).toBeTruthy()
  })

  it('adds a webhook destination: create disabled, test, enable', async () => {
    vi.mocked(createDestination).mockResolvedValue('d2')
    vi.mocked(testDestination).mockResolvedValue({ ok: true })
    vi.mocked(patchDestination).mockResolvedValue()

    renderTab()
    fireEvent.click(await screen.findByRole('link', { name: /^Webhook/ }))
    fireEvent.change(await screen.findByPlaceholderText('ops-inbox'), { target: { value: 'ops-hook' } })
    fireEvent.change(screen.getByPlaceholderText('https://…/hook'), {
      target: { value: 'https://example.com/hook' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Test send' }))

    const addButton = await screen.findByRole('button', { name: 'Add destination' })
    await waitFor(() => expect((addButton as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(addButton)

    await waitFor(() => expect(patchDestination).toHaveBeenCalledWith('d2', { enabled: true }))
    expect(createDestination).toHaveBeenCalledWith({
      name: 'ops-hook',
      kind: 'webhook',
      config: { url: 'https://example.com/hook', format: 'json' },
      enabled: false,
    })
  })

  it('keeps a failing webhook destination disabled and says why', async () => {
    vi.mocked(createDestination).mockResolvedValue('d2')
    vi.mocked(testDestination).mockResolvedValue({ ok: false, error: 'status 500' })

    renderTab()
    fireEvent.click(await screen.findByRole('link', { name: /^Webhook/ }))
    fireEvent.change(await screen.findByPlaceholderText('ops-inbox'), { target: { value: 'ops-hook' } })
    fireEvent.change(screen.getByPlaceholderText('https://…/hook'), {
      target: { value: 'https://example.com/hook' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Test send' }))

    expect(await screen.findByText(/Test failed: status 500/)).toBeTruthy()
    expect(patchDestination).not.toHaveBeenCalled()
    expect((screen.getByRole('button', { name: 'Add destination' }) as HTMLButtonElement).disabled).toBe(
      true,
    )
  })

  it('adds an email destination with a picked google connector', async () => {
    vi.mocked(createDestination).mockResolvedValue('d3')
    vi.mocked(testDestination).mockResolvedValue({ ok: true })
    vi.mocked(patchDestination).mockResolvedValue()

    renderTab()
    fireEvent.click(await screen.findByRole('link', { name: /^Email/ }))
    fireEvent.change(await screen.findByPlaceholderText('ops-inbox'), { target: { value: 'ops-inbox' } })
    fireEvent.click(await screen.findByText('Choose a connected Gmail account'))
    fireEvent.click(await screen.findByRole('option', { name: 'gmail' }))
    fireEvent.change(screen.getByPlaceholderText('ops@example.com'), {
      target: { value: 'ops@example.com' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Test send' }))

    const addButton = await screen.findByRole('button', { name: 'Add destination' })
    await waitFor(() => expect((addButton as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(addButton)

    await waitFor(() => expect(patchDestination).toHaveBeenCalledWith('d3', { enabled: true }))
    expect(createDestination).toHaveBeenCalledWith({
      name: 'ops-inbox',
      kind: 'email',
      config: { connector_id: 'c1', to: 'ops@example.com' },
      enabled: false,
    })
  })

  it('adds a telegram destination: writes the bot token secret then creates', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createDestination).mockResolvedValue('d4')
    vi.mocked(testDestination).mockResolvedValue({ ok: true })
    vi.mocked(patchDestination).mockResolvedValue()

    renderTab()
    fireEvent.click(await screen.findByRole('link', { name: /^Telegram/ }))
    fireEvent.change(await screen.findByPlaceholderText('ops-inbox'), { target: { value: 'ops-telegram' } })
    fireEvent.change(screen.getByPlaceholderText('123456789'), { target: { value: '123456' } })
    fireEvent.change(screen.getByPlaceholderText('123456:ABC-DEF...'), { target: { value: 'bot-token-value' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test send' }))

    const addButton = await screen.findByRole('button', { name: 'Add destination' })
    await waitFor(() => expect((addButton as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(addButton)

    await waitFor(() => expect(patchDestination).toHaveBeenCalledWith('d4', { enabled: true }))
    expect(setSecret).toHaveBeenCalledWith('OPS_TELEGRAM_TELEGRAM_BOT_TOKEN', 'bot-token-value')
    expect(createDestination).toHaveBeenCalledWith({
      name: 'ops-telegram',
      kind: 'telegram',
      config: { chat_id: '123456' },
      credential_ref: 'OPS_TELEGRAM_TELEGRAM_BOT_TOKEN',
      enabled: false,
    })
  })

  it('reuses an existing credential for a telegram bot token, skipping the secret write', async () => {
    vi.mocked(listSecretRefs).mockResolvedValue([{ name: 'SHARED_BOT_TOKEN', backend: 'db', referenced_by: [] }])
    vi.mocked(createDestination).mockResolvedValue('d4')
    vi.mocked(testDestination).mockResolvedValue({ ok: true })

    renderTab()
    fireEvent.click(await screen.findByRole('link', { name: /^Telegram/ }))
    fireEvent.change(await screen.findByPlaceholderText('ops-inbox'), { target: { value: 'ops-telegram' } })
    fireEvent.change(screen.getByPlaceholderText('123456789'), { target: { value: '123456' } })
    fireEvent.click(screen.getByRole('radio', { name: 'Use existing' }))
    fireEvent.click(await screen.findByLabelText('existing credential'))
    fireEvent.click(await screen.findByRole('option', { name: 'SHARED_BOT_TOKEN' }))
    fireEvent.click(screen.getByRole('button', { name: 'Test send' }))

    await waitFor(() => expect(testDestination).toHaveBeenCalled())
    expect(setSecret).not.toHaveBeenCalled()
    expect(createDestination).toHaveBeenCalledWith({
      name: 'ops-telegram',
      kind: 'telegram',
      config: { chat_id: '123456' },
      credential_ref: 'SHARED_BOT_TOKEN',
      enabled: false,
    })
  })

  it('edits a telegram destination config and rotates its bot token', async () => {
    vi.mocked(listDestinations).mockResolvedValue([telegramDestination])
    vi.mocked(patchDestination).mockResolvedValue()
    vi.mocked(setSecret).mockResolvedValue()

    renderTab('/settings/destinations/d4')

    const chatIDInput = await screen.findByDisplayValue('123456')
    fireEvent.change(chatIDInput, { target: { value: '987654' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(patchDestination).toHaveBeenCalledWith('d4', { config: { chat_id: '987654' } }),
    )

    const tokenInput = screen.getByPlaceholderText('123456:ABC-DEF...')
    expect(tokenInput).toHaveAttribute('type', 'password')
    fireEvent.change(tokenInput, { target: { value: 'new-token' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save token' }))
    await waitFor(() => expect(setSecret).toHaveBeenCalledWith('OPS_TELEGRAM_TELEGRAM_BOT_TOKEN', 'new-token'))
    expect(patchDestination).toHaveBeenCalledWith('d4', { credential_ref: 'OPS_TELEGRAM_TELEGRAM_BOT_TOKEN' })
  })

  it('stages destination config edits: zero PATCH before Save, one on Save, Cancel discards', async () => {
    vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
    vi.mocked(patchDestination).mockResolvedValue()

    renderTab(`/settings/destinations/${webhookDestination.id}`)

    const urlInput = await screen.findByDisplayValue('https://example.com/hook')
    fireEvent.change(urlInput, { target: { value: 'https://example.com/hook2' } })
    expect(patchDestination).not.toHaveBeenCalled()
    expect(screen.getByText('Unsaved changes')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(patchDestination).not.toHaveBeenCalled()
    expect(screen.getByDisplayValue('https://example.com/hook')).toBeTruthy()

    fireEvent.change(screen.getByDisplayValue('https://example.com/hook'), {
      target: { value: 'https://example.com/hook2' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(patchDestination).toHaveBeenCalledWith('d1', {
        config: { url: 'https://example.com/hook2', format: 'json' },
      }),
    )
    expect(patchDestination).toHaveBeenCalledTimes(1)
  })

  it('toggles Enabled from the edit page and refreshes on success', async () => {
    vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
    vi.mocked(patchDestination).mockResolvedValue()

    renderTab(`/settings/destinations/${webhookDestination.id}`)
    const toggle = await screen.findByRole('switch', { name: 'ops-hook enabled' })
    fireEvent.click(toggle)

    await waitFor(() => expect(patchDestination).toHaveBeenCalledWith('d1', { enabled: false }))
  })

  it('toggles Enabled from the edit page and toasts on failure', async () => {
    vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
    vi.mocked(patchDestination).mockRejectedValue(new Error('locked'))

    renderTab(`/settings/destinations/${webhookDestination.id}`)
    fireEvent.click(await screen.findByRole('switch', { name: 'ops-hook enabled' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not update destination', { description: 'locked' }),
    )
  })

  it('toasts and keeps the confirm state cleared when delete fails from the edit page', async () => {
    vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
    vi.mocked(deleteDestination).mockRejectedValue(new Error('still referenced'))

    renderTab(`/settings/destinations/${webhookDestination.id}`)
    fireEvent.click(await screen.findByRole('button', { name: 'Delete' }))
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not remove destination', {
        description: 'still referenced',
      }),
    )
  })

  it('shows the failure reason for a thrown (non-API-shaped) test error on the edit page', async () => {
    vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
    vi.mocked(testDestination).mockRejectedValue(new Error('timeout'))

    renderTab(`/settings/destinations/${webhookDestination.id}`)
    fireEvent.click(await screen.findByRole('button', { name: 'Test send' }))

    expect(await screen.findByText(/Failed: timeout/)).toBeTruthy()
  })

  it('switches the bot token to a different credential and rotates it', async () => {
    vi.mocked(listDestinations).mockResolvedValue([telegramDestination])
    vi.mocked(listSecretRefs).mockResolvedValue([
      { name: 'SHARED_BOT_TOKEN', backend: 'db', referenced_by: [] },
    ])
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(patchDestination).mockResolvedValue()

    renderTab(`/settings/destinations/${telegramDestination.id}`)
    await screen.findByDisplayValue('123456')

    fireEvent.click(screen.getByRole('radio', { name: 'Different credential' }))
    fireEvent.click(await screen.findByLabelText('existing credential'))
    fireEvent.click(await screen.findByRole('option', { name: /SHARED_BOT_TOKEN/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Save token' }))

    await waitFor(() => expect(patchDestination).toHaveBeenCalledWith('d4', { credential_ref: 'SHARED_BOT_TOKEN' }))
    expect(setSecret).not.toHaveBeenCalled()
  })

  it('toasts when rotating the bot token fails', async () => {
    vi.mocked(listDestinations).mockResolvedValue([telegramDestination])
    vi.mocked(setSecret).mockRejectedValue(new Error('store unavailable'))

    renderTab(`/settings/destinations/${telegramDestination.id}`)
    fireEvent.change(await screen.findByPlaceholderText('123456:ABC-DEF...'), { target: { value: 'new-token' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save token' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not update bot token', { description: 'store unavailable' }),
    )
  })

  it('shows a persistent Alert with Retry when saving destination config fails', async () => {
    vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
    vi.mocked(patchDestination).mockRejectedValueOnce(new Error('server unavailable'))

    renderTab(`/settings/destinations/${webhookDestination.id}`)

    const urlInput = await screen.findByDisplayValue('https://example.com/hook')
    fireEvent.change(urlInput, { target: { value: 'https://example.com/hook2' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText(/server unavailable/)).toBeTruthy()
    expect(screen.getByDisplayValue('https://example.com/hook2')).toBeTruthy()

    vi.mocked(patchDestination).mockResolvedValue()
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(patchDestination).toHaveBeenCalledTimes(2))
  })

  it('every Select and Input in DestinationAdd and DestinationEdit is found by label', async () => {
    vi.mocked(listDestinations).mockResolvedValue([telegramDestination])
    renderTab('/settings/destinations/d4')
    expect(await screen.findByLabelText('Chat ID')).toBeTruthy()

    cleanup()
    renderTab()
    fireEvent.click(await screen.findByRole('link', { name: /^GitHub/ }))
    expect(await screen.findByLabelText('Name')).toBeTruthy()
    expect(screen.getByLabelText('GitHub connector')).toBeTruthy()
    expect(screen.getByLabelText('Mode')).toBeTruthy()
    expect(screen.getByLabelText('Branch pattern')).toBeTruthy()
    expect(screen.getByLabelText('Commit style')).toBeTruthy()
  })

  it('toasts when loading connectors for the email/github add form fails', async () => {
    vi.mocked(listConnectors).mockRejectedValue(new Error('network down'))
    renderTab()
    fireEvent.click(await screen.findByRole('link', { name: /^Email/ }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not load connectors', { description: 'network down' }),
    )
  })

  it('redirects to the destinations list for an unknown kind', async () => {
    render(
      <MemoryRouter initialEntries={['/settings/destinations/new/carrier-pigeon']}>
        <Routes>
          <Route path="/settings/destinations/*" element={<DestinationsTab />} />
        </Routes>
      </MemoryRouter>,
    )
    expect(await screen.findByText('No destinations yet')).toBeTruthy()
  })

  it('shows the test failure reason and allows retesting a webhook add', async () => {
    vi.mocked(createDestination).mockResolvedValue('d2')
    vi.mocked(testDestination).mockResolvedValueOnce({ ok: false, error: 'refused' })
    renderTab()

    fireEvent.click(await screen.findByRole('link', { name: /^Webhook/ }))
    fireEvent.change(await screen.findByPlaceholderText('ops-inbox'), { target: { value: 'ops-hook' } })
    fireEvent.change(screen.getByPlaceholderText('https://…/hook'), {
      target: { value: 'https://example.com/hook' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Test send' }))
    expect(await screen.findByText(/Test failed: refused/)).toBeTruthy()

    vi.mocked(testDestination).mockResolvedValueOnce({ ok: true })
    fireEvent.click(screen.getByRole('button', { name: 'Test send' }))
    await waitFor(() => expect(testDestination).toHaveBeenCalledTimes(2))
  })

  it('toasts when runTest throws while creating the destination row', async () => {
    vi.mocked(createDestination).mockRejectedValue(new Error('quota exceeded'))
    renderTab()

    fireEvent.click(await screen.findByRole('link', { name: /^Webhook/ }))
    fireEvent.change(await screen.findByPlaceholderText('ops-inbox'), { target: { value: 'ops-hook' } })
    fireEvent.change(screen.getByPlaceholderText('https://…/hook'), {
      target: { value: 'https://example.com/hook' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Test send' }))

    expect(await screen.findByText(/Test failed: quota exceeded/)).toBeTruthy()
  })

  it('toasts when creating a github destination fails', async () => {
    vi.mocked(createDestination).mockRejectedValue(new Error('name taken'))
    renderTab()

    fireEvent.click(await screen.findByRole('link', { name: /^GitHub/ }))
    fireEvent.change(await screen.findByPlaceholderText('ops-inbox'), { target: { value: 'ops-repo' } })
    fireEvent.click(await screen.findByLabelText('GitHub connector'))
    fireEvent.click(await screen.findByRole('option', { name: 'my-github' }))
    fireEvent.click(await screen.findByLabelText('Mode'))
    fireEvent.click(await screen.findByRole('option', { name: 'Push and open a PR when done' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Add destination' }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not add destination', { description: 'name taken' }),
    )
  })

  it('toasts when enabling a tested destination fails', async () => {
    vi.mocked(createDestination).mockResolvedValue('d2')
    vi.mocked(testDestination).mockResolvedValue({ ok: true })
    vi.mocked(patchDestination).mockRejectedValue(new Error('gone'))
    renderTab()

    fireEvent.click(await screen.findByRole('link', { name: /^Webhook/ }))
    fireEvent.change(await screen.findByPlaceholderText('ops-inbox'), { target: { value: 'ops-hook' } })
    fireEvent.change(screen.getByPlaceholderText('https://…/hook'), {
      target: { value: 'https://example.com/hook' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Test send' }))
    const addButton = await screen.findByRole('button', { name: 'Add destination' })
    await waitFor(() => expect((addButton as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(addButton)

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not enable destination', { description: 'gone' }),
    )
  })

  it('Cancel navigates back to the destinations list from the add form', async () => {
    renderTab()
    fireEvent.click(await screen.findByRole('link', { name: /^Webhook/ }))
    await screen.findByPlaceholderText('ops-inbox')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(await screen.findByText('No destinations yet')).toBeTruthy()
    expect(createDestination).not.toHaveBeenCalled()
  })

  it('offers the GitHub tile in the add flow', async () => {
    renderTab()
    expect(await screen.findByRole('link', { name: /^GitHub/ })).toBeTruthy()
  })

  it('sets branch pattern, commit style, and create-if-missing on a github add', async () => {
    vi.mocked(createDestination).mockResolvedValue('d5')
    renderTab()

    fireEvent.click(await screen.findByRole('link', { name: /^GitHub/ }))
    fireEvent.change(await screen.findByPlaceholderText('ops-inbox'), { target: { value: 'ops-repo' } })
    fireEvent.click(await screen.findByLabelText('GitHub connector'))
    fireEvent.click(await screen.findByRole('option', { name: 'my-github' }))
    fireEvent.click(await screen.findByLabelText('Mode'))
    fireEvent.click(await screen.findByRole('option', { name: 'Push and open a PR when done' }))
    fireEvent.change(screen.getByLabelText('Branch pattern'), { target: { value: '{type}/{slug}' } })
    fireEvent.click(screen.getByLabelText('Commit style'))
    fireEvent.click(await screen.findByRole('option', { name: 'Plain' }))
    fireEvent.click(screen.getByLabelText('Create repository if missing'))

    const addButton = await screen.findByRole('button', { name: 'Add destination' })
    await waitFor(() => expect((addButton as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(addButton)

    await waitFor(() =>
      expect(createDestination).toHaveBeenCalledWith({
        name: 'ops-repo',
        kind: 'github',
        config: {
          connector_id: 'c2',
          mode: 'push_pr',
          branch_pattern: '{type}/{slug}',
          commit_style: 'plain',
          create_if_missing: true,
        },
        enabled: true,
      }),
    )
  })

  it('adds a github destination: no test-send, creates enabled directly', async () => {
    vi.mocked(createDestination).mockResolvedValue('d5')

    renderTab()
    fireEvent.click(await screen.findByRole('link', { name: /^GitHub/ }))
    fireEvent.change(await screen.findByPlaceholderText('ops-inbox'), { target: { value: 'ops-repo' } })

    // No test-send affordance for github.
    expect(screen.queryByRole('button', { name: 'Test send' })).toBeNull()

    // Connector + mode required before submit is enabled.
    const addButton = screen.getByRole('button', { name: 'Add destination' })
    expect((addButton as HTMLButtonElement).disabled).toBe(true)

    fireEvent.click(await screen.findByLabelText('GitHub connector'))
    fireEvent.click(await screen.findByRole('option', { name: 'my-github' }))
    fireEvent.click(await screen.findByLabelText('Mode'))
    fireEvent.click(await screen.findByRole('option', { name: 'Push and open a PR when done' }))

    await waitFor(() => expect((addButton as HTMLButtonElement).disabled).toBe(false))
    fireEvent.click(addButton)

    await waitFor(() =>
      expect(createDestination).toHaveBeenCalledWith({
        name: 'ops-repo',
        kind: 'github',
        config: { connector_id: 'c2', mode: 'push_pr' },
        enabled: true,
      }),
    )
  })

  it('renders a github destination card with mode and no Test send button', async () => {
    vi.mocked(listDestinations).mockResolvedValue([githubDestination])
    renderTab()

    expect(await screen.findByText('ops-repo')).toBeTruthy()
    expect(await screen.findByText('push + PR via my-github')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Test send' })).toBeNull()
  })

  it('redirects to the destinations list when the id is not found', async () => {
    renderTab('/settings/destinations/missing')
    expect(await screen.findByText('No destinations yet')).toBeTruthy()
  })

  it('shows a toast when loading the destination list fails', async () => {
    vi.mocked(listDestinations).mockRejectedValue(new Error('network down'))
    renderTab(`/settings/destinations/${webhookDestination.id}`)

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith('Could not load destination', { description: 'network down' }),
    )
  })

  it('deletes a github destination behind confirm and navigates back', async () => {
    vi.mocked(listDestinations).mockResolvedValueOnce([githubDestination]).mockResolvedValueOnce([])
    vi.mocked(deleteDestination).mockResolvedValue()
    renderTab(`/settings/destinations/${githubDestination.id}`)

    await screen.findByRole('heading', { name: 'ops-repo' })
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(deleteDestination).toHaveBeenCalledWith('d5'))
    expect(await screen.findByText('No destinations yet')).toBeTruthy()
  })

  it('edits a github destination config and saves the github-shaped patch', async () => {
    vi.mocked(listDestinations).mockResolvedValue([githubDestination])
    vi.mocked(patchDestination).mockResolvedValue()

    renderTab(`/settings/destinations/${githubDestination.id}`)

    fireEvent.click(await screen.findByLabelText('Mode'))
    fireEvent.click(await screen.findByRole('option', { name: 'Push branch when done' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() =>
      expect(patchDestination).toHaveBeenCalledWith('d5', {
        config: {
          connector_id: 'c2',
          mode: 'push',
          branch_pattern: undefined,
          commit_style: undefined,
          create_if_missing: undefined,
        },
      }),
    )
  })

  it('test-sends from the edit page and shows the failure reason', async () => {
    vi.mocked(listDestinations).mockResolvedValue([webhookDestination])
    vi.mocked(testDestination).mockResolvedValue({ ok: false, error: 'timeout' })
    renderTab(`/settings/destinations/${webhookDestination.id}`)

    fireEvent.click(await screen.findByRole('button', { name: 'Test send' }))
    expect(await screen.findByText(/Failed: timeout/)).toBeTruthy()

    vi.mocked(testDestination).mockResolvedValue({ ok: true })
    fireEvent.click(screen.getByRole('button', { name: 'Test send' }))
    await waitFor(() => expect(testDestination).toHaveBeenCalledTimes(2))
  })

  it('loads a github destination config into the edit form', async () => {
    vi.mocked(listDestinations).mockResolvedValue([githubDestination])

    render(
      <MemoryRouter initialEntries={['/settings/destinations/d5']}>
        <Routes>
          <Route path="/settings/destinations/*" element={<DestinationsTab />} />
        </Routes>
      </MemoryRouter>,
    )

    expect(await screen.findByRole('combobox', { name: 'GitHub connector' })).toHaveTextContent('my-github')
    expect(screen.getByRole('combobox', { name: 'Mode' })).toHaveTextContent('Push and open a PR when done')
    expect(screen.queryByRole('button', { name: 'Test send' })).toBeNull()
  })
})
