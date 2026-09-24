import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminAgent, Channel, ChannelPairing } from '../../api/types'
import { TooltipProvider } from '../ui/tooltip'
import { ChannelsTab } from './ChannelsTab'

vi.mock('../../api/client', () => ({
  approveChannelPairing: vi.fn(),
  createChannel: vi.fn(),
  deleteChannel: vi.fn(),
  getChannel: vi.fn(),
  listAgents: vi.fn(),
  listChannelPairings: vi.fn(),
  listChannels: vi.fn(),
  listSecretBackends: vi.fn(),
  listSecretRefs: vi.fn(),
  patchChannel: vi.fn(),
  revokeChannelPairing: vi.fn(),
  setSecret: vi.fn(),
  testChannel: vi.fn(),
}))

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))

import {
  approveChannelPairing,
  createChannel,
  deleteChannel,
  getChannel,
  listAgents,
  listChannelPairings,
  listChannels,
  listSecretBackends,
  listSecretRefs,
  patchChannel,
  revokeChannelPairing,
  setSecret,
  testChannel,
} from '../../api/client'

const agent: AdminAgent = {
  id: 'a1',
  name: 'Researcher',
  description: '',
  prompt_overlay: '',
  route: '',
  skills: [],
  tools: [],
  memory: true,
} as unknown as AdminAgent

const channel: Channel = {
  id: 'c1',
  name: 'my-telegram',
  kind: 'telegram',
  config: { dispatch: false, bot_username: 'timothy_bot' },
  credential_ref: 'MY_TELEGRAM_CHANNEL_BOT_TOKEN',
  agent_id: 'a1',
  enabled: true,
  pairings: { pending: 1, approved: 2, revoked: 0 },
  created_at: '2026-09-24T00:00:00Z',
  updated_at: '2026-09-24T00:00:00Z',
}

function pairing(over: Partial<ChannelPairing>): ChannelPairing {
  return {
    channel_id: 'c1',
    external_user_id: '77',
    display_name: 'Ada',
    status: 'pending',
    created_at: '2026-09-24T00:00:00Z',
    updated_at: '2026-09-24T00:00:00Z',
    ...over,
  }
}

function renderTab(entry = '/settings/channels') {
  return render(
    <TooltipProvider>
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path="/settings/channels/*" element={<ChannelsTab />} />
        </Routes>
      </MemoryRouter>
    </TooltipProvider>,
  )
}

afterEach(cleanup)
beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn()
  vi.clearAllMocks()
  vi.mocked(listChannels).mockResolvedValue([])
  vi.mocked(listAgents).mockResolvedValue([agent])
  vi.mocked(listSecretBackends).mockResolvedValue([{ backend: 'db', configured: true, default: true }])
  vi.mocked(listSecretRefs).mockResolvedValue([])
  vi.mocked(listChannelPairings).mockResolvedValue([])
  vi.mocked(getChannel).mockResolvedValue(channel)
})

describe('Channels settings', () => {
  it('shows the empty state and the Telegram add tile', async () => {
    renderTab()
    expect(await screen.findByText('No channels yet')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Telegram' }).getAttribute('href')).toBe('/settings/channels/new')
  })

  it('lists a channel with its bot, agent and pairing counts', async () => {
    vi.mocked(listChannels).mockResolvedValue([channel])
    renderTab()
    expect(await screen.findByText('Your channels · 1')).toBeTruthy()
    expect(screen.getByText('@timothy_bot')).toBeTruthy()
    expect(await screen.findByText('Researcher · 1 pending · 2 approved')).toBeTruthy()
  })

  it('toggles, tests and deletes from the card', async () => {
    vi.mocked(listChannels).mockResolvedValue([channel])
    vi.mocked(patchChannel).mockResolvedValue({ ...channel, enabled: false })
    vi.mocked(testChannel).mockResolvedValue({ ok: true, bot_username: 'timothy_bot' })
    vi.mocked(deleteChannel).mockResolvedValue()
    renderTab()

    fireEvent.click(await screen.findByRole('switch', { name: 'my-telegram enabled' }))
    await waitFor(() => expect(patchChannel).toHaveBeenCalledWith('c1', { enabled: false }))

    fireEvent.click(screen.getByRole('button', { name: 'Test' }))
    expect(await screen.findByText('Connected as @timothy_bot')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(deleteChannel).toHaveBeenCalledWith('c1'))
  })

  it('adds a channel: writes the token, creates disabled, tests, then enables', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createChannel).mockResolvedValue('c9')
    vi.mocked(testChannel).mockResolvedValue({ ok: true, bot_username: 'new_bot' })
    vi.mocked(patchChannel).mockResolvedValue(channel)
    renderTab('/settings/channels/new')

    const add = await screen.findByRole('button', { name: 'Add channel' })
    expect((add as HTMLButtonElement).disabled).toBe(true)
    fireEvent.change(screen.getByPlaceholderText('my-telegram'), { target: { value: 'My Telegram' } })
    fireEvent.change(screen.getByLabelText('Bot token'), { target: { value: '123:abc' } })
    expect(screen.getByText(/long-polls Telegram/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    expect(await screen.findByText(/Connected as @new_bot/)).toBeTruthy()
    expect(setSecret).toHaveBeenCalledWith('MY_TELEGRAM_CHANNEL_BOT_TOKEN', '123:abc')
    expect(createChannel).toHaveBeenCalledWith({
      name: 'My Telegram',
      kind: 'telegram',
      credential_ref: 'MY_TELEGRAM_CHANNEL_BOT_TOKEN',
      agent_id: undefined,
      config: { dispatch: false },
      enabled: false,
    })
    expect(testChannel).toHaveBeenCalledWith('c9')

    fireEvent.click(screen.getByRole('button', { name: 'Add channel' }))
    await waitFor(() => expect(patchChannel).toHaveBeenCalledWith('c9', { enabled: true }))
  })

  it('retries a failed test by updating the saved channel instead of creating another', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createChannel).mockResolvedValue('c9')
    vi.mocked(testChannel).mockRejectedValueOnce(new Error('telegram getMe: status 401: Unauthorized'))
    vi.mocked(testChannel).mockResolvedValueOnce({ ok: true, bot_username: 'new_bot' })
    vi.mocked(patchChannel).mockResolvedValue(channel)
    renderTab('/settings/channels/new')

    fireEvent.change(await screen.findByPlaceholderText('my-telegram'), { target: { value: 'bot' } })
    fireEvent.change(screen.getByLabelText('Bot token'), { target: { value: 'bad' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText(/Test failed: telegram getMe: status 401/)).toBeTruthy()

    fireEvent.change(screen.getByLabelText('Bot token'), { target: { value: 'good' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText(/Connected as @new_bot/)).toBeTruthy()
    expect(createChannel).toHaveBeenCalledTimes(1)
    expect(patchChannel).toHaveBeenCalledWith('c9', {
      name: 'bot',
      credential_ref: 'BOT_CHANNEL_BOT_TOKEN',
      agent_id: null,
      config: { dispatch: false },
    })
  })

  it('shows a pending code with its expiry and approves or revokes senders', async () => {
    const expires = new Date(Date.now() + 5 * 60_000 + 30_000).toISOString()
    vi.mocked(listChannelPairings).mockResolvedValue([
      pairing({ code: '482913', code_expires_at: expires }),
      pairing({ external_user_id: '88', display_name: 'Bob', status: 'approved' }),
    ])
    vi.mocked(approveChannelPairing).mockResolvedValue()
    vi.mocked(revokeChannelPairing).mockResolvedValue()
    renderTab('/settings/channels/c1')

    expect(await screen.findByText('482913')).toBeTruthy()
    expect(screen.getByText(/expires in 5:\d\d/)).toBeTruthy()
    expect(screen.getByText('Pending')).toBeTruthy()
    expect(screen.getByText('Approved')).toBeTruthy()

    const adaRow = screen.getByText('Ada').closest('tr')!
    fireEvent.click(within(adaRow).getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(approveChannelPairing).toHaveBeenCalledWith('c1', '77'))

    const bobRow = screen.getByText('Bob').closest('tr')!
    expect(within(bobRow).queryByRole('button', { name: 'Approve' })).toBeNull()
    fireEvent.click(within(bobRow).getByRole('button', { name: 'Revoke' }))
    await waitFor(() => expect(revokeChannelPairing).toHaveBeenCalledWith('c1', '88'))
  })

  it('saves name and agent changes as one patch', async () => {
    vi.mocked(patchChannel).mockResolvedValue({ ...channel, name: 'renamed' })
    renderTab('/settings/channels/c1')
    const name = await screen.findByDisplayValue('my-telegram')
    fireEvent.change(name, { target: { value: 'renamed' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(patchChannel).toHaveBeenCalledWith('c1', { name: 'renamed', agent_id: 'a1', config: { dispatch: false } }),
    )
  })

  it('shows the empty pairings state', async () => {
    renderTab('/settings/channels/c1')
    expect(await screen.findByText('No one has messaged this bot yet')).toBeTruthy()
  })
})
