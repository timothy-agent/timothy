import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminAgent, AdminConnector, Channel, ChannelPairing } from '../../api/types'
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
  listConnectors: vi.fn(),
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
  listConnectors,
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

function imapConnector(over: Partial<AdminConnector>): AdminConnector {
  return {
    id: 'k1',
    name: 'mailbox',
    kind: 'imap',
    config: { host: 'imap.example.com', username: 'timothy@example.com', smtp_host: 'smtp.example.com' },
    credential_ref: 'MAILBOX_IMAP_PASSWORD',
    enabled: true,
    sensitive: false,
    ...over,
  }
}

const emailChannel: Channel = {
  ...channel,
  id: 'c3',
  name: 'inbox',
  kind: 'email',
  credential_ref: '',
  config: { dispatch: false, bot_username: 'timothy@example.com', connector_id: 'k1', from_allow: ['ada@x.com', '@team.org'] },
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
  vi.mocked(listConnectors).mockResolvedValue([])
})

describe('Channels settings', () => {
  it('shows the empty state and the Telegram and Slack add tiles', async () => {
    renderTab()
    expect(await screen.findByText('No channels yet')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Telegram' }).getAttribute('href')).toBe('/settings/channels/new')
    expect(screen.getByRole('link', { name: 'Slack' }).getAttribute('href')).toBe('/settings/channels/new?kind=slack')
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

  it('adds a Slack channel: writes both tokens and sends the app token ref', async () => {
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(createChannel).mockResolvedValue('c7')
    vi.mocked(testChannel).mockResolvedValue({ ok: true, bot_username: 'timothy' })
    vi.mocked(patchChannel).mockResolvedValue(channel)
    renderTab('/settings/channels/new?kind=slack')

    fireEvent.change(await screen.findByPlaceholderText('my-slack'), { target: { value: 'Team Slack' } })
    expect(screen.getByText(/connects to Slack over Socket Mode/)).toBeTruthy()
    fireEvent.change(screen.getByLabelText('Bot token'), { target: { value: 'xoxb-1' } })
    const test = screen.getByRole('button', { name: 'Test connection' }) as HTMLButtonElement
    expect(test.disabled).toBe(true)
    fireEvent.change(screen.getByLabelText('App token'), { target: { value: 'xapp-1' } })
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    expect(await screen.findByText(/Connected as @timothy/)).toBeTruthy()
    expect(setSecret).toHaveBeenCalledWith('TEAM_SLACK_CHANNEL_BOT_TOKEN', 'xoxb-1')
    expect(setSecret).toHaveBeenCalledWith('TEAM_SLACK_CHANNEL_APP_TOKEN', 'xapp-1')
    expect(createChannel).toHaveBeenCalledWith({
      name: 'Team Slack',
      kind: 'slack',
      credential_ref: 'TEAM_SLACK_CHANNEL_BOT_TOKEN',
      agent_id: undefined,
      config: { dispatch: false, app_token_ref: 'TEAM_SLACK_CHANNEL_APP_TOKEN' },
      enabled: false,
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add channel' }))
    await waitFor(() => expect(patchChannel).toHaveBeenCalledWith('c7', { enabled: true }))
  })

  it('rotates a Slack app token on the manage page', async () => {
    const slackChannel: Channel = {
      ...channel,
      id: 'c2',
      name: 'team-slack',
      kind: 'slack',
      credential_ref: 'TEAM_SLACK_CHANNEL_BOT_TOKEN',
      config: { dispatch: false, bot_username: 'timothy', app_token_ref: 'TEAM_SLACK_CHANNEL_APP_TOKEN' },
    }
    vi.mocked(getChannel).mockResolvedValue(slackChannel)
    vi.mocked(setSecret).mockResolvedValue()
    vi.mocked(patchChannel).mockResolvedValue(slackChannel)
    renderTab('/settings/channels/c2')

    fireEvent.change(await screen.findByLabelText('App token'), { target: { value: 'xapp-new' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save app token' }))
    await waitFor(() =>
      expect(patchChannel).toHaveBeenCalledWith('c2', { config: { app_token_ref: 'TEAM_SLACK_CHANNEL_APP_TOKEN' } }),
    )
    expect(setSecret).toHaveBeenCalledWith('TEAM_SLACK_CHANNEL_APP_TOKEN', 'xapp-new')
    expect(screen.getByText('Checks the bot token with Slack.')).toBeTruthy()
  })

  it('shows no app token panel for a Telegram channel', async () => {
    renderTab('/settings/channels/c1')
    expect(await screen.findByText('Rotate bot token')).toBeTruthy()
    expect(screen.queryByText('Rotate app token')).toBeNull()
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

  it('offers an Email add tile', async () => {
    renderTab()
    expect((await screen.findByRole('link', { name: 'Email' })).getAttribute('href')).toBe('/settings/channels/new?kind=email')
  })

  it('adds an email channel: picks the SMTP-capable IMAP connector, no token, allowlist in config', async () => {
    vi.mocked(listConnectors).mockResolvedValue([
      imapConnector({ id: 'k0', name: 'no-smtp', config: { host: 'h', username: 'u' } }),
      imapConnector({}),
      imapConnector({ id: 'k2', name: 'off', enabled: false }),
      imapConnector({ id: 'g1', name: 'gmail', kind: 'google' }),
    ])
    vi.mocked(createChannel).mockResolvedValue('c8')
    vi.mocked(testChannel).mockResolvedValue({ ok: true, bot_username: 'timothy@example.com' })
    vi.mocked(patchChannel).mockResolvedValue(emailChannel)
    renderTab('/settings/channels/new?kind=email')

    fireEvent.change(await screen.findByPlaceholderText('my-email'), { target: { value: 'Inbox' } })
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'IMAP connector' }).textContent).toBe('mailbox'))
    expect(screen.queryByLabelText('Bot token')).toBeNull()
    expect((screen.getByRole('button', { name: 'Test connection' }) as HTMLButtonElement).disabled).toBe(true)
    const allow = screen.getByLabelText('From allowlist')
    fireEvent.change(allow, { target: { value: 'Ada@X.com, @team.org' } })
    fireEvent.keyDown(allow, { key: 'Enter' })
    expect(screen.getByRole('button', { name: 'Remove sender ada@x.com' })).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }))

    expect(await screen.findByText('Connected to timothy@example.com, ready to add.')).toBeTruthy()
    expect(setSecret).not.toHaveBeenCalled()
    expect(createChannel).toHaveBeenCalledWith({
      name: 'Inbox',
      kind: 'email',
      credential_ref: '',
      agent_id: undefined,
      config: { dispatch: false, connector_id: 'k1', from_allow: ['ada@x.com', '@team.org'] },
      enabled: false,
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add channel' }))
    await waitFor(() => expect(patchChannel).toHaveBeenCalledWith('c8', { enabled: true }))
  })

  it('points to connectors when no IMAP connector can reply', async () => {
    vi.mocked(listConnectors).mockResolvedValue([imapConnector({ config: { host: 'h', username: 'u' } })])
    renderTab('/settings/channels/new?kind=email')
    const link = await screen.findByRole('link', { name: 'Add an IMAP connector with SMTP first' })
    expect(link.getAttribute('href')).toBe('/settings/connectors')
    fireEvent.change(screen.getByPlaceholderText('my-email'), { target: { value: 'Inbox' } })
    expect((screen.getByRole('button', { name: 'Test connection' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('edits an email channel allowlist on the manage page', async () => {
    vi.mocked(getChannel).mockResolvedValue(emailChannel)
    vi.mocked(listConnectors).mockResolvedValue([imapConnector({})])
    vi.mocked(patchChannel).mockResolvedValue(emailChannel)
    renderTab('/settings/channels/c3')

    fireEvent.click(await screen.findByRole('button', { name: 'Remove sender @team.org' }))
    const allow = screen.getByLabelText('From allowlist')
    fireEvent.change(allow, { target: { value: 'bob@y.org' } })
    fireEvent.keyDown(allow, { key: 'Enter' })
    expect(screen.queryByText('Rotate bot token')).toBeNull()
    expect(screen.getByText('Checks the mailbox with its IMAP connector.')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(patchChannel).toHaveBeenCalledWith('c3', {
        name: 'inbox',
        agent_id: 'a1',
        config: { dispatch: false, connector_id: 'k1', from_allow: ['ada@x.com', 'bob@y.org'] },
      }),
    )
  })
})
