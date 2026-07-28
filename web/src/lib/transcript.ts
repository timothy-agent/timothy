import type { TranscriptItem } from '../api/types'
import type { AssistantState, ToolRun } from './chat'

// ChatItem is one renderable unit of the chat page: live turns and
// replayed transcript items share this shape so resume is
// pixel-equivalent with the original stream.
export type ChatItem = { id: string } & (
  | { role: 'user'; text: string }
  | ({ role: 'assistant' } & AssistantState)
  | { role: 'compaction'; text: string }
  | { role: 'interrupted'; text: string }
)

function toToolRun(tool: NonNullable<TranscriptItem['tool']>): ToolRun {
  const status = tool.status
  return {
    id: tool.call_id,
    name: tool.name,
    args: tool.args,
    status: status === 'ok' || status === 'error' || status === 'denied' ? status : 'ok',
    digest: tool.result_digest,
    durationMs: tool.duration_ms,
  }
}

// fromTranscript maps the server's UI replay projection into chat
// items. The transcript hides nothing: compaction dividers and
// interrupted turns render alongside the conversation. Tool calls no
// longer get their own item — a flush-based pass folds each run of
// `kind: 'tool'` items into the assistant item that follows, matching
// the live `AssistantState` shape (`tools[]` on the turn). A run of
// tool items with no following assistant turn (trailing tool loop, or
// one cut short by a user/compaction/interrupted item) still flushes,
// as a tools-only assistant item so replay never drops executed calls.
export function fromTranscript(items: TranscriptItem[]): ChatItem[] {
  const out: ChatItem[] = []
  let pending: { seq: number; tool: NonNullable<TranscriptItem['tool']> }[] = []

  const flush = () => {
    if (pending.length === 0) return
    const tools = pending.map((p) => toToolRun(p.tool))
    out.push({
      id: `replay-${pending[0].seq}`,
      role: 'assistant',
      text: '',
      reasoning: '',
      notices: [],
      tools,
      permissions: [],
      streaming: false,
      meta: undefined,
    })
    pending = []
  }

  for (const item of items) {
    const id = `replay-${item.seq}`
    switch (item.kind) {
      case 'user':
        flush()
        out.push({ id, role: 'user', text: item.text ?? '' })
        break
      case 'tool':
        if (item.tool) pending.push({ seq: item.seq, tool: item.tool })
        break
      case 'assistant': {
        let text = ''
        let reasoning = ''
        for (const b of item.blocks ?? []) {
          // An empty block serializes without its text key (omitempty);
          // naive concat would render a literal "undefined".
          if (b.type === 'text') text += b.text ?? ''
          else if (b.type === 'reasoning') reasoning += b.text ?? ''
        }
        const tools = pending.map((p) => toToolRun(p.tool))
        pending = []
        out.push({
          id,
          role: 'assistant',
          text,
          reasoning,
          notices: [],
          tools,
          permissions: [],
          streaming: false,
          meta: item.provider
            ? { provider: item.provider, model: item.model, usage: item.usage }
            : undefined,
        })
        break
      }
      case 'compaction':
        flush()
        out.push({ id, role: 'compaction', text: item.text ?? '' })
        break
      case 'interrupted':
        flush()
        out.push({ id, role: 'interrupted', text: item.text ?? '' })
        break
    }
  }
  flush()
  return out
}
