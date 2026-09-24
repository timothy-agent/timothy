import type { AdminAgent } from '../../api/types'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '../ui/select'
import { DEFAULT_AGENT, DISPATCH_AGENT } from './channelAgent'

// ChannelAgentSelect picks who answers on a channel: the default agent,
// per-message dispatch, or one agent.
export function ChannelAgentSelect({
  value,
  onChange,
  agents,
}: {
  value: string
  onChange: (value: string) => void
  agents: AdminAgent[]
}) {
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger className="w-full" aria-label="Agent">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={DEFAULT_AGENT}>Default agent</SelectItem>
        <SelectItem value={DISPATCH_AGENT}>Dispatch automatically</SelectItem>
        {agents.map((a) => (
          <SelectItem key={a.id} value={a.id}>
            {a.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
