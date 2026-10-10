import type { ImageRef, Transcript, TranscriptItem } from '../api/types'
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
    status:
      status === 'ok' || status === 'error' || status === 'denied' || status === 'canceled'
        ? status
        : 'ok',
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
        // An empty block serializes without its text key (omitempty);
        // it is skipped so it never becomes a blank note or a literal
        // "undefined".
        const segments: string[] = []
        let reasoning = ''
        let media: NonNullable<(typeof item)['blocks']>[number]['media'] = undefined
        for (const b of item.blocks ?? []) {
          if (b.type === 'text') {
            if (b.text) segments.push(b.text)
          } else if (b.type === 'reasoning') reasoning += b.text ?? ''
          else if (b.type === 'media' && b.media) media = [...(media ?? []), ...b.media]
        }
        const tools = pendingTools.map((p) => toToolRun(p.tool))
        const permissions = pendingPermissions.map((p) => p.permission)
        pendingTools = []
        pendingPermissions = []
        // With tool runs, every segment but the last is a step note
        // (the narration line before a tool call) and the last is the
        // answer. Without them the segments stay one message.
        const split = tools.length > 0 && segments.length > 1
        const text = split ? segments[segments.length - 1] : segments.join('\n\n')
        const stepNotes = split ? segments.slice(0, -1) : undefined
        out.push({
          id,
          role: 'assistant',
          text,
          stepNotes,
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

// TranscriptWindow is the server transcript a chat page holds, merged
// across seq pages (issue #1113). Cursors are event seqs from the
// page responses, not item seqs: some events render no item.
export interface TranscriptWindow {
  sessionId: string
  items: TranscriptItem[]
  oldestSeq?: number
  newestSeq?: number
  hasOlder: boolean
}

// reconcile drops held items that later events hide in a full
// projection: an interrupted item once its pending_state is no longer
// live, and an ask once answered.
function reconcile(items: TranscriptItem[], page: Transcript): TranscriptItem[] {
  const resolved = new Set(page.resolved_permissions ?? [])
  return items.filter(
    (i) =>
      !(i.kind === 'interrupted' && i.seq !== page.live_pending_seq) &&
      !(i.kind === 'permission' && i.permission && resolved.has(i.permission.id)),
  )
}

// windowFromPage starts a window from a latest-N page.
export function windowFromPage(sessionId: string, page: Transcript): TranscriptWindow {
  return {
    sessionId,
    items: page.items,
    oldestSeq: page.first_seq,
    newestSeq: page.last_seq,
    hasOlder: page.has_more ?? false,
  }
}

// mergePage folds an older (before_seq) or newer (after_seq) page into
// w, deduped by seq in seq order. Only a newer page reconciles held
// items: hiding events always come after what they hide, and the next
// newer page catches up anything an older one could report. It returns
// w itself when nothing changed, so callers can skip a re-render.
export function mergePage(w: TranscriptWindow, page: Transcript, direction: 'older' | 'newer'): TranscriptWindow {
  const bySeq = new Map(w.items.map((i) => [i.seq, i]))
  for (const i of page.items) if (!bySeq.has(i.seq)) bySeq.set(i.seq, i)
  const sorted = [...bySeq.values()].sort((a, b) => a.seq - b.seq)
  const items = direction === 'newer' ? reconcile(sorted, page) : sorted
  const oldest = Math.min(w.oldestSeq ?? Infinity, page.first_seq ?? Infinity)
  const newest = Math.max(w.newestSeq ?? 0, page.last_seq ?? 0)
  const next: TranscriptWindow = {
    sessionId: w.sessionId,
    items,
    oldestSeq: oldest === Infinity ? undefined : oldest,
    newestSeq: newest === 0 ? undefined : newest,
    hasOlder: direction === 'older' ? (page.has_more ?? false) : w.hasOlder,
  }
  const same =
    items.length === w.items.length &&
    items.every((it, i) => it === w.items[i]) &&
    next.hasOlder === w.hasOlder &&
    next.oldestSeq === w.oldestSeq &&
    next.newestSeq === w.newestSeq
  return same ? w : next
}

// prependChatItems renders an older page in front of current, the
// rendered list whose head is beforeDerived (fromTranscript of the
// window before the merge); mergedDerived is fromTranscript of the
// merged window. Only the boundary item can differ from a full replay
// (it may fold the older page's trailing tool calls), so everything
// after it, live or optimistic items included, is kept as is.
// current[0] is replaced only while it is the untouched replay.
export function prependChatItems(
  current: ChatItem[],
  beforeDerived: ChatItem[],
  mergedDerived: ChatItem[],
): ChatItem[] {
  if (beforeDerived.length === 0) return [...mergedDerived, ...current]
  const head = mergedDerived.slice(0, mergedDerived.length - (beforeDerived.length - 1))
  if (current[0] !== beforeDerived[0]) return [...head.slice(0, -1), ...current]
  return [...head, ...current.slice(1)]
}
