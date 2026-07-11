// Mirrors the brain's SSE wire contract: normalized gateway events
// followed by exactly one terminal meta event — read until meta.

export interface Usage {
  input_tokens: number
  output_tokens: number
  cache_read_tokens?: number
  cache_write_tokens?: number
}

export interface ToolCallEvent {
  id: string
  name: string
  input?: unknown
}

export interface ToolResultEvent {
  id: string
  name: string
  status: 'ok' | 'error' | 'denied'
  digest?: string
  duration_ms: number
}

export interface PermissionRequestEvent {
  id: string
  call_id: string
  tool: string
  args: string
  danger_level: 'safe' | 'destructive'
  rationale: string
}

export interface StreamEvent {
  type:
    | 'chunk'
    | 'reasoning_chunk'
    | 'tool_start'
    | 'tool_end'
    | 'tool_result'
    | 'permission_request'
    | 'usage'
    | 'retry'
    | 'incomplete'
    | 'done'
    | 'error'
  text?: string
  tool_call?: ToolCallEvent
  tool_result?: ToolResultEvent
  permission?: PermissionRequestEvent
  usage?: Usage
  error?: { code: string; message: string; retryable: boolean }
  retry?: { attempt: number; backoff_ms: number; reason: string }
  meta?: { provider: string; model: string; ledger_id?: string }
}

export interface MetaEvent {
  type: 'meta'
  session_id: string
  provider?: string
  model?: string
  usage?: Usage
  ledger_id?: string
}

export type ChatEvent = StreamEvent | MetaEvent

export interface ChatRequest {
  session_id?: string
  message: string
  task_category?: string
  model_hint?: string
}

// --- session management (mirrors brain's /v1/sessions surface) ---

export interface SessionMeta {
  id: string
  title: string
  archived: boolean
  last_category?: string
  created_at: string
  updated_at: string
}

export interface UIBlock {
  type: 'text' | 'reasoning'
  text: string
}

// One executed tool call in the replay projection (digest only).
export interface ToolExecution {
  call_id: string
  name: string
  args?: string
  result_digest?: string
  status: string
  duration_ms?: number
}

// One renderable unit of the UI replay projection. The transcript
// hides nothing: compactions and interrupted turns are items too.
export interface TranscriptItem {
  seq: number
  kind: 'user' | 'assistant' | 'tool' | 'compaction' | 'interrupted'
  text?: string
  blocks?: UIBlock[]
  tool?: ToolExecution
  provider?: string
  model?: string
  usage?: Usage
  created_at: string
}

export interface Transcript {
  session: SessionMeta
  items: TranscriptItem[]
}

// One long-term memory row as served by the management API.
export interface MemoryItem {
  id: string
  type: 'episodic' | 'semantic' | 'procedural'
  content: string
  status: 'pending' | 'active' | 'rejected' | 'archived'
  confidence: number
  actor: string
  source_session?: string
  created_at: string
  superseded_by?: string
}

// One hybrid-retrieval hit (memory browser search).
export interface RetrievedMemory {
  id: string
  type: string
  content: string
  score: number
}
