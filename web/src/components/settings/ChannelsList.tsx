import { Mail } from 'lucide-react'
import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { toast } from 'sonner'
import { deleteChannel, listAgents, listChannels, patchChannel, testChannel } from '../../api/client'
import type { AdminAgent, Channel } from '../../api/types'
import { SlackIcon } from '../icons/SlackIcon'
import { TelegramIcon } from '../icons/TelegramIcon'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Switch } from '../ui/switch'
import { ConfirmDialog } from '../timothy/confirm-dialog'
import { EmptyState } from '../timothy/empty-state'
import { PageHeader, SectionHeader } from '../timothy/page-header'
import { PageShell } from '../timothy/page-shell'
import { AddPresetTile } from './AddPresetTile'
import { EntityCard } from './EntityCard'
import { channelAgentLabel } from './channelAgent'
import { settingsArea } from './settingsAreas'
import { TestStatus } from './TestStatus'
import { errText } from '../../lib/errors'

const area = settingsArea('channels')

function channelTile(kind: Channel['kind']): ReactNode {
  if (kind === 'slack') return <SlackIcon className="size-9" />
  if (kind === 'email') return <Mail className="size-9" />
  return <TelegramIcon className="size-9" />
}

export function ChannelsList() {
  const [channels, setChannels] = useState<Channel[]>([])
  const [agents, setAgents] = useState<AdminAgent[]>([])

  const refresh = useCallback(() => {
    listChannels()
      .then(setChannels)
      .catch((err: unknown) => toast.error('Could not load channels', { description: errText(err) }))
  }, [])
  useEffect(refresh, [refresh])

  useEffect(() => {
    listAgents()
      .then(setAgents)
      .catch(() => {
        // Non-fatal: cards fall back to "Unknown agent".
      })
  }, [])

  return (
    <PageShell>
      <PageHeader
        title={area.label}
        description={area.description}
        breadcrumbs={[{ label: 'Settings', href: '/settings' }, { label: area.label }]}
      />
      <div className="space-y-10">
        <section className="space-y-4">
          <SectionHeader title={channels.length > 0 ? `Your channels · ${channels.length}` : 'Your channels'} />
          <p className="-mt-2 max-w-2xl text-sm text-muted-foreground">
            Timothy polls Telegram, holds a Socket Mode connection to Slack and polls email inboxes over IMAP, so
            nothing inbound needs exposing. Unknown senders get a pairing prompt and never reach a model until you
            approve them here.
          </p>
          {channels.length === 0 ? (
            <EmptyState title="No channels yet" description="Add one below." />
          ) : (
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {channels.map((c) => (
                <ChannelCard key={c.id} channel={c} agents={agents} onChanged={refresh} />
              ))}
            </div>
          )}
        </section>

        <section className="space-y-4">
          <SectionHeader title="Add a channel" />
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            <AddPresetTile
              to="/settings/channels/new"
              title="Telegram"
              description="Chat with Timothy through a Telegram bot."
              tile={<TelegramIcon className="size-9" />}
            />
            <AddPresetTile
              to="/settings/channels/new?kind=slack"
              title="Slack"
              description="Chat with Timothy through a Slack app."
              tile={<SlackIcon className="size-9" />}
            />
            <AddPresetTile
              to="/settings/channels/new?kind=email"
              title="Email"
              description="Chat with Timothy by email over an IMAP connector."
              tile={<Mail className="size-9" />}
            />
          </div>
        </section>
      </div>
    </PageShell>
  )
}

function ChannelCard({ channel, agents, onChanged }: { channel: Channel; agents: AdminAgent[]; onChanged: () => void }) {
  const [testing, setTesting] = useState(false)
  const [test, setTest] = useState<{ ok: boolean; message: string } | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const toggle = (enabled: boolean) => {
    patchChannel(channel.id, { enabled }).then(onChanged, (err: unknown) =>
      toast.error('Could not update channel', { description: errText(err) }),
    )
  }

  const runTest = async () => {
    setTesting(true)
    setTest(null)
    try {
      const res = await testChannel(channel.id)
      const who = channel.kind === 'email' ? `to ${res.bot_username}` : `as @${res.bot_username}`
      setTest({ ok: true, message: `Connected ${who}` })
      onChanged()
    } catch (err) {
      setTest({ ok: false, message: `Failed: ${errText(err)}` })
    } finally {
      setTesting(false)
    }
  }

  const remove = async () => {
    try {
      await deleteChannel(channel.id)
      toast.success('Channel removed')
      onChanged()
    } catch (err) {
      toast.error('Could not remove channel', { description: errText(err) })
    } finally {
      setConfirmDelete(false)
    }
  }

  const { pending, approved } = channel.pairings
  return (
    <>
      <EntityCard
        to={`/settings/channels/${channel.id}`}
        title={channel.name}
        tile={channelTile(channel.kind)}
        badges={
          <Badge variant="outline" size="sm">
            {channel.kind}
          </Badge>
        }
        summary={
          <div className="space-y-1 text-xs text-muted-foreground">
            <div className="truncate">
              {channel.config.bot_username ? (
                <span className="font-mono">{`${channel.kind === 'email' ? '' : '@'}${channel.config.bot_username}`}</span>
              ) : (
                'Not tested yet'
              )}
            </div>
            <div className="truncate">
              {channelAgentLabel(channel, agents)} · {pending} pending · {approved} approved
            </div>
          </div>
        }
        status={test && <TestStatus state={test.ok ? 'ok' : 'failed'} message={test.message} />}
        footer={
          <>
            <Switch checked={channel.enabled} onCheckedChange={toggle} aria-label={`${channel.name} enabled`} />
            <Button size="sm" variant="test" disabled={testing} onClick={() => void runTest()} className="flex-1">
              {testing ? 'Testing…' : 'Test'}
            </Button>
            <Button size="sm" variant="destructive" onClick={() => setConfirmDelete(true)}>
              Delete
            </Button>
          </>
        }
      />
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete ${channel.name}?`}
        description="Stops the bot and removes its pairings. Chat sessions it started stay in your history."
        confirmLabel="Delete"
        destructive
        onConfirm={() => void remove()}
      />
    </>
  )
}
