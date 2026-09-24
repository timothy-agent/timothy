import type { AdminAgent, Channel, ChannelPatch } from '../../api/types'

// Select values for the two choices that are not an agent id.
export const DEFAULT_AGENT = 'default'
export const DISPATCH_AGENT = 'dispatch'

// channelAgentValue maps a channel to its agent Select value.
export function channelAgentValue(channel: Pick<Channel, 'agent_id' | 'config'>): string {
  if (channel.agent_id) return channel.agent_id
  return channel.config.dispatch ? DISPATCH_AGENT : DEFAULT_AGENT
}

// channelAgentPatch maps an agent Select value to agent_id and dispatch.
export function channelAgentPatch(value: string): Pick<ChannelPatch, 'agent_id' | 'config'> {
  if (value === DISPATCH_AGENT) return { agent_id: null, config: { dispatch: true } }
  if (value === DEFAULT_AGENT) return { agent_id: null, config: { dispatch: false } }
  return { agent_id: value, config: { dispatch: false } }
}

// channelAgentLabel names who answers on a channel, for card summaries.
export function channelAgentLabel(channel: Pick<Channel, 'agent_id' | 'config'>, agents: AdminAgent[]): string {
  const value = channelAgentValue(channel)
  if (value === DISPATCH_AGENT) return 'Dispatch automatically'
  if (value === DEFAULT_AGENT) return 'Default agent'
  return agents.find((a) => a.id === value)?.name ?? 'Unknown agent'
}
