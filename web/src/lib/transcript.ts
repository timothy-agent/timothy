import type { ImageRef, TranscriptItem } from '../api/types'
import type { AssistantState, ToolRun } from './chat'

// ChatItem is one renderable unit of the chat page: live turns and
// replayed transcript items share this shape so resume is
// pixel-equivalent with the original stream.
export type ChatItem = { id: string } & (
  | { role: 'user'; text: string; images?: ImageRef[]; documents?: ImageRef[] }
  | ({ role: 'assistant' } & AssistantState)
  | { role: 'compaction'; text: string }
  | { role: 'interrupted'; text: string }
  | { role: 'error'; text: string }
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
// `kind: 'permission'` items (a still-unresolved ask — the projection
// already dropped answered ones) fold the same way, landing in
// `permissions[]` so the existing PermissionModal renders on reload
// exactly as it does live.
export function fromTranscript(items: TranscriptItem[]): ChatItem[] {
  const out: ChatItem[] = []
  let pendingTools: { seq: number; tool: NonNullable<TranscriptItem['tool']> }[] = []
  let pendingPermissions: { seq: number; permission: NonNullable<TranscriptItem['permission']> }[] = []

  const flush = () => {
    if (pendingTools.length === 0 && pendingPermissions.length === 0) return
    const tools = pendingTools.map((p) => toToolRun(p.tool))
    const permissions = pendingPermissions.map((p) => p.permission)
    const seq = Math.min(
      ...pendingTools.map((p) => p.seq),
      ...pendingPermissions.map((p) => p.seq),
    )
    out.push({
      id: `replay-${seq}`,
      role: 'assistant',
      text: '',
      reasoning: '',
      notices: [],
      tools,
      permissions,
      media: [],
      streaming: false,
      meta: undefined,
    })
    pendingTools = []
    pendingPermissions = []
  }

  for (const item of items) {
    const id = `replay-${item.seq}`
    switch (item.kind) {
      case 'user':
        flush()
        out.push({
          id,
          role: 'user',
          text: item.text ?? '',
          images: item.images && item.images.length > 0 ? item.images : undefined,
          documents: item.documents && item.documents.length > 0 ? item.documents : undefined,
        })
        break
      case 'tool':
        if (item.tool) pendingTools.push({ seq: item.seq, tool: item.tool })
        break
      case 'permission':
        if (item.permission) pendingPermissions.push({ seq: item.seq, permission: item.permission })
        break
      case 'assistant': {
        let text = ''
        let reasoning = ''
        let media: NonNullable<(typeof item)['blocks']>[number]['media'] = undefined
        for (const b of item.blocks ?? []) {
          // An empty block serializes without its text key (omitempty);
          // naive concat would render a literal "undefined".
          if (b.type === 'text') text += (text && b.text ? '\n\n' : '') + (b.text ?? '')
          else if (b.type === 'reasoning') reasoning += b.text ?? ''
          else if (b.type === 'media' && b.media) media = [...(media ?? []), ...b.media]
        }
        const tools = pendingTools.map((p) => toToolRun(p.tool))
        const permissions = pendingPermissions.map((p) => p.permission)
        pendingTools = []
        pendingPermissions = []
        out.push({
          id,
          role: 'assistant',
          text,
          reasoning,
          notices: [],
          tools,
          permissions,
          media: media ?? [],
          streaming: false,
          meta: item.provider
            ? {
                provider: item.provider,
                model: item.model,
                usage: item.usage,
                durationMs: item.duration_ms,
                cost: item.cost,
                currency: item.currency,
                convertedCost: item.converted_cost,
                convertedCurrency: item.converted_currency,
                rateAsOf: item.rate_as_of,
              }
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
      case 'error':
        flush()
        out.push({ id, role: 'error', text: item.text ?? '' })
        break
    }
  }
  flush()
  return out
}
