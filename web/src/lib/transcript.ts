import type { TranscriptItem } from '../api/types'
import type { AssistantState, ToolRun } from './chat'

// ChatItem is one renderable unit of the chat page: live turns and
// replayed transcript items share this shape so resume is
// pixel-equivalent with the original stream.
export type ChatItem = { id: string } & (
  | { role: 'user'; text: string }
  | ({ role: 'assistant' } & AssistantState)
  | { role: 'tool'; tool: ToolRun }
  | { role: 'compaction'; text: string }
  | { role: 'interrupted'; text: string }
)

// fromTranscript maps the server's UI replay projection into chat
// items. The transcript hides nothing: compaction dividers and
// interrupted turns, and executed tool calls render alongside the
// conversation.
export function fromTranscript(items: TranscriptItem[]): ChatItem[] {
  const out: ChatItem[] = []
  for (const item of items) {
    const id = `replay-${item.seq}`
    switch (item.kind) {
      case 'user':
        out.push({ id, role: 'user', text: item.text ?? '' })
        break
      case 'tool': {
        if (!item.tool) break
        const status = item.tool.status
        out.push({
          id,
          role: 'tool',
          tool: {
            id: item.tool.call_id,
            name: item.tool.name,
            args: item.tool.args,
            status: status === 'ok' || status === 'error' || status === 'denied' ? status : 'ok',
            digest: item.tool.result_digest,
            durationMs: item.tool.duration_ms,
          },
        })
        break
      }
      case 'assistant': {
        let text = ''
        let reasoning = ''
        for (const b of item.blocks ?? []) {
          if (b.type === 'text') text += b.text
          else if (b.type === 'reasoning') reasoning += b.text
        }
        out.push({
          id,
          role: 'assistant',
          text,
          reasoning,
          notices: [],
          tools: [],
          permissions: [],
          streaming: false,
          meta: item.provider
            ? { provider: item.provider, model: item.model, usage: item.usage }
            : undefined,
        })
        break
      }
      case 'compaction':
        out.push({ id, role: 'compaction', text: item.text ?? '' })
        break
      case 'interrupted':
        out.push({ id, role: 'interrupted', text: item.text ?? '' })
        break
    }
  }
  return out
}
