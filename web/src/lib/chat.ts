import type { ChatEvent, PermissionRequestEvent, Usage } from '../api/types'

// ToolRun is one tool call's lifecycle inside a live turn.
export interface ToolRun {
  id: string
  name: string
  args?: string
  status: 'running' | 'ok' | 'error' | 'denied'
  digest?: string
  durationMs?: number
  // The permission prompt (if any) this call parked on, so its result
  // clears exactly that prompt and no other parallel one.
  permissionId?: string
}

export interface AssistantState {
  text: string
  reasoning: string
  notices: string[]
  tools: ToolRun[]
  permissions: PermissionRequestEvent[]
  error?: string
  meta?: { provider?: string; model?: string; usage?: Usage }
  streaming: boolean
}

export function emptyAssistant(): AssistantState {
  return { text: '', reasoning: '', notices: [], tools: [], permissions: [], streaming: true }
}

// applyEvent folds one SSE event into the assistant message state.
export function applyEvent(msg: AssistantState, ev: ChatEvent): AssistantState {
  switch (ev.type) {
    case 'chunk':
      return { ...msg, text: msg.text + (ev.text ?? '') }
    case 'reasoning_chunk':
      return { ...msg, reasoning: msg.reasoning + (ev.text ?? '') }
    case 'tool_start': {
      if (!ev.tool_call) return msg
      return {
        ...msg,
        tools: [...msg.tools, { id: ev.tool_call.id, name: ev.tool_call.name, status: 'running' }],
      }
    }
    case 'tool_end': {
      if (!ev.tool_call) return msg
      const { id, input } = ev.tool_call
      return {
        ...msg,
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
        tools: msg.tools.map((t) =>
          t.id === r.id
            ? { ...t, status: r.status, digest: r.digest, durationMs: r.duration_ms }
            : t,
        ),
        // Clear only the prompt tied to THIS call; other parallel
        // calls may still be parked awaiting their own decision.
        permissions: msg.permissions.filter((p) => p.call_id !== r.id),
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
    case 'incomplete':
      return { ...msg, notices: [...msg.notices, `incomplete: ${ev.text || 'stream cut off'}`] }
    case 'error':
      return { ...msg, error: `${ev.error?.code}: ${ev.error?.message}` }
    case 'meta':
      return {
        ...msg,
        streaming: false,
        permissions: [],
        meta: { provider: ev.provider, model: ev.model, usage: ev.usage },
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
