import type { TranscriptItem } from '../api/types'
import type { AssistantState } from './chat'

// ChatItem is one renderable unit of the chat page: live turns and
// replayed transcript items share this shape so resume is
// pixel-equivalent with the original stream.
export type ChatItem = { id: string } & (
  | { role: 'user'; text: string }
  | ({ role: 'assistant' } & AssistantState)
  | { role: 'compaction'; text: string }
  | { role: 'interrupted'; text: string }
)

// fromTranscript maps the server's UI replay projection into chat
// items. The transcript hides nothing: compaction dividers and
// interrupted turns render alongside the conversation. Tool items are
// skipped until the tool loop lands (no fixture produces them yet).
export function fromTranscript(items: TranscriptItem[]): ChatItem[] {
  const out: ChatItem[] = []
  for (const item of items) {
    const id = `replay-${item.seq}`
    switch (item.kind) {
      case 'user':
        out.push({ id, role: 'user', text: item.text ?? '' })
        break
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
          streaming: false,
          meta: item.provider ? { provider: item.provider, model: item.model } : undefined,
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
