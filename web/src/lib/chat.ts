import type { ChatEvent, MediaRef, PermissionRequestEvent, Usage } from '../api/types'

// ToolRun is one tool call's lifecycle inside a live turn.
export interface ToolRun {
  id: string
  name: string
  args?: string
  status: 'running' | 'ok' | 'error' | 'denied' | 'canceled'
  digest?: string
  durationMs?: number
  // The permission prompt (if any) this call parked on, so its result
  // clears exactly that prompt and no other parallel one.
  permissionId?: string
}

export interface AssistantState {
  text: string
  // stepNotes are the turn's earlier agent-loop text segments (the
  // short narration lines before each tool call); `text` holds only
  // the final segment, the answer.
  stepNotes?: string[]
  // sawTool marks a tool event landed since the last text chunk.
  // Mirrors brain's segment rule: the next chunk after a tool event,
  // with text already present, opens a new agent-loop segment.
  sawTool?: boolean
  reasoning: string
  notices: string[]
  tools: ToolRun[]
  permissions: PermissionRequestEvent[]
  // media accumulates every ref a tool call emitted this turn, in
  // call-result order.
  media: MediaRef[]
  error?: string
  stopped?: boolean
  meta?: {
    provider?: string
    model?: string
    usage?: Usage
    durationMs?: number
    cost?: number | null
    currency?: string
    convertedCost?: number
    convertedCurrency?: string
    rateAsOf?: string
  }
  streaming: boolean
}

export function emptyAssistant(): AssistantState {
  return { text: '', reasoning: '', notices: [], tools: [], permissions: [], media: [], streaming: true }
}

// applyEvent folds one SSE event into the assistant message state.
export function applyEvent(msg: AssistantState, ev: ChatEvent): AssistantState {
  switch (ev.type) {
    case 'chunk': {
      // A chunk arriving after a tool event, with text already in
      // hand, closes the previous segment: it becomes a step note and
      // text restarts from this chunk. Brain persists the same split.
      if (msg.sawTool && msg.text !== '')
        return {
          ...msg,
          stepNotes: [...(msg.stepNotes ?? []), msg.text],
          text: ev.text ?? '',
          sawTool: false,
        }
      return { ...msg, text: msg.text + (ev.text ?? ''), sawTool: false }
    }
    case 'reasoning_chunk':
      return { ...msg, reasoning: msg.reasoning + (ev.text ?? '') }
    case 'tool_start': {
      if (!ev.tool_call) return msg
      return {
        ...msg,
        sawTool: true,
        tools: [...msg.tools, { id: ev.tool_call.id, name: ev.tool_call.name, status: 'running' }],
      }
    }
    case 'tool_end': {
      if (!ev.tool_call) return msg
      const { id, input } = ev.tool_call
      return {
        ...msg,
        sawTool: true,
        tools: msg.tools.map((t) =>
          t.id === id ? { ...t, args: input === undefined ? t.args : JSON.stringify(input) } : t,
        ),
      }
    }
    case 'tool_result': {
      if (!ev.tool_result) return msg
      const r = ev.tool_result
      return {
        ...msg,
        sawTool: true,
        tools: msg.tools.map((t) =>
          t.id === r.id
            ? { ...t, status: r.status, digest: r.digest, durationMs: r.duration_ms }
            : t,
        ),
        // Clear only the prompt tied to THIS call; other parallel
        // calls may still be parked awaiting their own decision.
        permissions: msg.permissions.filter((p) => p.call_id !== r.id),
        media: r.media && r.media.length > 0 ? [...msg.media, ...r.media] : msg.media,
      }
    }
    case 'permission_request': {
      if (!ev.permission) return msg
      return { ...msg, permissions: [...msg.permissions, ev.permission] }
    }
    case 'retry':
      return {
        ...msg,
        notices: [...msg.notices, `retrying (attempt ${ev.retry?.attempt}): ${ev.retry?.reason}`],
      }
    case 'failover': {
      const f = ev.failover
      if (!f) return msg
      return {
        ...msg,
        notices: [
          ...msg.notices,
          `${f.from_provider} failed (${f.code}), switching to ${f.to_provider}`,
        ],
      }
    }
    case 'incomplete':
      return { ...msg, notices: [...msg.notices, `incomplete: ${ev.text || 'stream cut off'}`] }
    case 'error':
      if (ev.error?.code === 'stopped') return { ...msg, stopped: true }
      return { ...msg, error: `${ev.error?.code}: ${ev.error?.message}` }
    case 'meta':
      return {
        ...msg,
        streaming: false,
        permissions: [],
        meta: {
          provider: ev.provider,
          model: ev.model,
          usage: ev.usage,
          durationMs: ev.duration_ms,
          cost: ev.cost,
          currency: ev.currency,
          convertedCost: ev.converted_cost,
          convertedCurrency: ev.converted_currency,
          rateAsOf: ev.rate_as_of,
        },
      }
    default:
      // done (terminal marker) and usage (folded into meta by brain)
      // intentionally leave the state unchanged.
      return msg
  }
}

// answerPermission removes one prompt locally once the decision is
// posted; the loop's next events carry the outcome.
export function answerPermission(msg: AssistantState, id: string): AssistantState {
  return { ...msg, permissions: msg.permissions.filter((p) => p.id !== id) }
}
