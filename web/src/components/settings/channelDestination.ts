import type { Channel } from '../../api/types'
import type { DestinationKindValues } from './DestinationKindFields'

// channelDestinationConfig is a channel destination's config: chat and
// optional thread for Telegram and Slack channels, to for email.
export function channelDestinationConfig(values: DestinationKindValues, channel?: Channel): Record<string, unknown> {
  if (channel?.kind === 'email') return { channel_id: values.channelID, to: values.to.trim() }
  return {
    channel_id: values.channelID,
    chat_id: values.chatID.trim(),
    ...(values.threadID.trim() && { thread_id: values.threadID.trim() }),
  }
}

// channelDestinationReady reports whether the fields the picked
// channel needs are filled.
export function channelDestinationReady(values: DestinationKindValues, channel?: Channel): boolean {
  if (!channel) return false
  return channel.kind === 'email' ? values.to.trim() !== '' : values.chatID.trim() !== ''
}
