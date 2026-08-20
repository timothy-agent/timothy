import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminConnector, Destination } from '../../api/types'
import { DestinationsTab } from './DestinationsTab'

vi.mock('../../api/client', () => ({
  createDestination: vi.fn(),
  deleteDestination: vi.fn(),
  listConnectors: vi.fn(),
  listDestinations: vi.fn(),
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

function renderTab(entry = '/settings/destinations') {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/settings/destinations/*" element={<DestinationsTab />} />
      </Routes>
    </MemoryRouter>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listDestinations).mockResolvedValue([])
  vi.mocked(listConnectors).mockResolvedValue([googleConnector])
  vi.mocked(listSecretRefs).mockResolvedValue([])
})

describe('Destinations tab', () => {
  it('shows the empty state and the add tiles when there are none', async () => {
    renderTab()
    expect(await screen.findByText('Your destinations')).toBeTruthy()
    expect(screen.getByText('No destinations yet, add one below.')).toBeTruthy()
    expect(screen.getByRole('button', { name: /^Email/ })).toBeTruthy()
    expect(screen.getByRole('button', { name: /^Webhook/ })).toBeTruthy()
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
    // The dialog's own confirm button is the second "Delete" in the
    // document once it's open (card's own button + dialog's).
    const confirmButtons = await screen.findAllByRole('button', { name: 'Delete' })
    fireEvent.click(confirmButtons[confirmButtons.length - 1])

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        'Could not remove destination',
        expect.objectContaining({ description: expect.stringContaining('referenced by an active mission') }),
      ),
    )
  })

  it('adds a webhook destination: create disabled, test, enable', async () => {
    vi.mocked(createDestination).mockResolvedValue('d2')
    vi.mocked(testDestination).mockResolvedValue({ ok: true })
    vi.mocked(patchDestination).mockResolvedValue()

    renderTab()
    fireEvent.click(await screen.findByRole('button', { name: /^Webhook/ }))
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
    fireEvent.click(await screen.findByRole('button', { name: /^Webhook/ }))
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
    fireEvent.click(await screen.findByRole('button', { name: /^Email/ }))
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
    fireEvent.click(await screen.findByRole('button', { name: /^Telegram/ }))
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
    fireEvent.click(await screen.findByRole('button', { name: /^Telegram/ }))
    fireEvent.change(await screen.findByPlaceholderText('ops-inbox'), { target: { value: 'ops-telegram' } })
    fireEvent.change(screen.getByPlaceholderText('123456789'), { target: { value: '123456' } })
    fireEvent.click(screen.getByRole('button', { name: 'Use existing' }))
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

    render(
      <MemoryRouter initialEntries={['/settings/destinations/d4']}>
        <Routes>
          <Route path="/settings/destinations/*" element={<DestinationsTab />} />
        </Routes>
      </MemoryRouter>,
    )

    const chatIDInput = await screen.findByDisplayValue('123456')
    fireEvent.change(chatIDInput, { target: { value: '987654' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }))
    await waitFor(() =>
      expect(patchDestination).toHaveBeenCalledWith('d4', { config: { chat_id: '987654' } }),
    )

    fireEvent.change(screen.getByPlaceholderText('123456:ABC-DEF...'), { target: { value: 'new-token' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save token' }))
    await waitFor(() => expect(setSecret).toHaveBeenCalledWith('OPS_TELEGRAM_TELEGRAM_BOT_TOKEN', 'new-token'))
    expect(patchDestination).toHaveBeenCalledWith('d4', { credential_ref: 'OPS_TELEGRAM_TELEGRAM_BOT_TOKEN' })
  })
})
