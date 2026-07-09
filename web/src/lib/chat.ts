import type { ChatEvent, Usage } from '../api/types'

export const categories = ['coding', 'reasoning', 'mini', 'summarize', 'realtime'] as const

export interface AssistantState {
  text: string
  reasoning: string
  notices: string[]
  error?: string
  meta?: { provider?: string; model?: string; usage?: Usage }
  streaming: boolean
}

// applyEvent folds one SSE event into the assistant message state.
export function applyEvent(msg: AssistantState, ev: ChatEvent): AssistantState {
  switch (ev.type) {
    case 'chunk':
      return { ...msg, text: msg.text + (ev.text ?? '') }
    case 'reasoning_chunk':
      return { ...msg, reasoning: msg.reasoning + (ev.text ?? '') }
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
        meta: { provider: ev.provider, model: ev.model, usage: ev.usage },
      }
    default:
      // done (terminal marker), usage (folded into meta by brain), and
      // tool_start/tool_end (no tool loop yet — extend here when it
      // lands) intentionally leave the state unchanged.
      return msg
  }
}
