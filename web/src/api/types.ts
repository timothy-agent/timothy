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
  agent?: string
  route?: string
  model_hint?: string
  skill_hint?: string
}

// --- session management (mirrors brain's /v1/sessions surface) ---

export interface SessionMeta {
  id: string
  title: string
  archived: boolean
  agent?: string
  last_route?: string
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

// Usage aggregates served by /v1/admin/usage/* — chart-ready, never
// raw ledger rows.
export interface UsageSummary {
  cost_usd: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  requests: number
  errors: number
}

export interface UsagePoint {
  bucket: string
  group: string
  cost_usd: number
  input_tokens: number
  output_tokens: number
  requests: number
  errors: number
}

export interface SessionUsage {
  session_id: string
  cost_usd: number
  input_tokens: number
  output_tokens: number
  requests: number
}

export interface LatencyRow {
  provider: string
  p50_ms: number
  p95_ms: number
  p99_ms: number
  requests: number
}

export interface CacheRow {
  provider: string
  cache_read_tokens: number
  input_tokens: number
  hit_ratio: number
}

// Budget position per UTC calendar window; limit_usd is null when no
// budget is configured.
export interface BudgetWindow {
  limit_usd: number | null
  spend_usd: number
  over: boolean
}

export interface BudgetStatus {
  day: BudgetWindow
  month: BudgetWindow
}

// Control-plane shapes (/v1/admin/*). credential_ref is a NAME (env
// var / Vault path / AWS profile) — secret values never travel.
export interface AdminModel {
  id: string
  context_window?: number
  capabilities?: string[]
  prices?: {
    input_per_mtok?: number
    output_per_mtok?: number
    cache_read_per_mtok?: number
    cache_write_per_mtok?: number
  }
}

export interface AdminProvider {
  id: string
  name: string
  kind: string
  driver: string
  base_url: string
  default_model: string
  models: AdminModel[]
  credential_ref: string
  headers: Record<string, string>
  enabled: boolean
}

export interface ChainEntry {
  provider_id: string
  model: string
}

export interface AdminRoute {
  name: string
  chain: ChainEntry[]
  // 'ordered' tries the chain as written; 'auto' | 'price' | 'latency'
  // score entries from recent ledger stats and declared prices.
  strategy: string
  enabled: boolean
}

export interface ProviderHealth {
  name: string
  enabled: boolean
  healthy: boolean
  last_success?: string
  last_error?: string
}

export interface TestResult {
  ok: boolean
  latency_ms: number
  model: string
  detail?: string
}

// AvailableModel is one model reported by a provider's own listing
// endpoint (GET /v1/admin/providers/:id/models).
export interface AvailableModel {
  id: string
}

// AdminAgent is one row of the agent registry (D-034): who serves a
// session. Empty skills/tools = everything allowed; empty route = the
// default chain.
export interface AdminAgent {
  id: string
  name: string
  description: string
  prompt_overlay: string
  route: string
  skills: string[]
  tools: string[]
  memory: boolean
  is_default: boolean
  enabled: boolean
}

// AdminConnector is one third-party integration the agent can call as
// tools (MCP server or Google account). config is kind-specific; the
// credential_ref names where its secret/tokens live.
export interface AdminConnector {
  id: string
  name: string
  kind: 'mcp' | 'google'
  config: Record<string, unknown>
  credential_ref: string
  enabled: boolean
}
