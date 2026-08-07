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

export interface PermissionResolvedEvent {
  id: string
  decision: string
}

export interface StreamEvent {
  type:
    | 'chunk'
    | 'reasoning_chunk'
    | 'tool_start'
    | 'tool_end'
    | 'tool_result'
    | 'permission_request'
    | 'permission_resolved'
    | 'usage'
    | 'retry'
    | 'failover'
    | 'incomplete'
    | 'done'
    | 'error'
  text?: string
  tool_call?: ToolCallEvent
  tool_result?: ToolResultEvent
  permission?: PermissionRequestEvent
  resolved?: PermissionResolvedEvent
  usage?: Usage
  error?: { code: string; message: string; retryable: boolean }
  retry?: { attempt: number; backoff_ms: number; reason: string }
  failover?: {
    from_provider: string
    from_model: string
    to_provider: string
    to_model: string
    code: string
  }
  meta?: { provider: string; model: string; ledger_id?: string }
}

export interface MetaEvent {
  type: 'meta'
  session_id: string
  provider?: string
  model?: string
  usage?: Usage
  ledger_id?: string
  duration_ms?: number
  // cost is null/absent when the gateway had no price for the serving
  // model — unknown price is never guessed (D-013); currency is blank
  // in that case too.
  cost?: number | null
  currency?: string
  // converted_cost/converted_currency/rate_as_of mirror ConvertedMoney
  // below: cost converted into the user's default_currency setting at
  // emit time, present only when it differs from currency and brain
  // had a usable stored fx rate. cost/currency above always stay the
  // billed truth (D-013); these are purely additive display fields.
  converted_cost?: number
  converted_currency?: string
  rate_as_of?: string
}

export type ChatEvent = StreamEvent | MetaEvent

export interface ChatRequest {
  session_id?: string
  message: string
  agent?: string
  route?: string
  model_hint?: string
  skill_hint?: string
  attachments?: string[]
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

// ImageRef is one attachment carried by a user transcript item — the
// attachment's id (content hash) and MIME type, never the bytes.
export interface ImageRef {
  id: string
  mime: string
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
// A 'permission' item is a still-unresolved ask (an answered one is
// dropped by the server projection, same as the live client drops it).
export interface TranscriptItem {
  seq: number
  kind: 'user' | 'assistant' | 'tool' | 'permission' | 'compaction' | 'interrupted' | 'error'
  text?: string
  blocks?: UIBlock[]
  images?: ImageRef[]
  // documents are attached PDFs, refs only (id+mime) — the converted
  // markdown never rides this payload.
  documents?: ImageRef[]
  tool?: ToolExecution
  permission?: PermissionRequestEvent
  provider?: string
  model?: string
  usage?: Usage
  duration_ms?: number
  // cost is null/absent when the gateway had no price for the serving
  // model — unknown price is never guessed (D-013); currency is blank
  // in that case too.
  cost?: number | null
  currency?: string
  // converted_cost/converted_currency/rate_as_of: same additive trio as
  // MetaEvent above, added by the server projection at serve time.
  converted_cost?: number
  converted_currency?: string
  rate_as_of?: string
  created_at: string
}

export interface Transcript {
  session: SessionMeta
  items: TranscriptItem[]
  // Whether a turn is currently streaming for this session server-side
  // — sourced from chat.Service's own turn registry, not a client
  // guess. Lets a tab that opens a session mid-turn know to attach
  // streamLive instead of rendering the last event as stale/interrupted.
  turn_active: boolean
}

// One unresolved permission ask, from GET /v1/permissions/pending —
// scoped server-side to sessions with a currently active turn.
export interface PendingPermission {
  session_id: string
  session_title: string
  tool: string
  rationale: string
  requested_at: string
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

// Entity graph (GET /v1/entities/graph): nodes are extracted entities
// with their active-memory counts; edges are co-occurrences (weight =
// shared active memories).
export interface EntityNode {
  id: string
  type: string
  name: string
  memory_count: number
}

export interface EntityEdge {
  src: string
  dst: string
  weight: number
}

export interface EntityGraphData {
  entities: EntityNode[]
  edges: EntityEdge[]
}

// ConvertedMoney is the additive trio brain's usage decorator
// (internal/brain/api/usage.go) adds next to a {cost|amount, currency}
// row: the same figure converted into the user's default_currency,
// present only when a stored fx rate exists for the row's currency
// (never a guess) and it differs from the target. The original
// cost/currency fields are always left exactly as the ledger recorded
// them (D-013) — these are purely additive display fields.
export interface ConvertedMoney {
  converted_amount?: number
  converted_currency?: string
  rate_as_of?: string
}

// Usage aggregates served by /v1/admin/usage/* — chart-ready, never
// raw ledger rows. Money fields are grouped by billing currency (no FX
// conversion anywhere): a range spanning more than one currency comes
// back as multiple rows, one per currency, never summed together.
export interface UsageSummary extends ConvertedMoney {
  currency: string
  cost: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  requests: number
  errors: number
  unpriced_requests: number
  unpriced_input_tokens: number
  unpriced_output_tokens: number
}

export interface UsagePoint extends ConvertedMoney {
  bucket: string
  group: string
  currency: string
  cost: number
  input_tokens: number
  output_tokens: number
  requests: number
  errors: number
  unpriced_input_tokens: number
  unpriced_output_tokens: number
}

// GroupTotal is one group's totals over a whole range — the
// non-time-bucketed sibling of UsagePoint, for tables/charts that rank
// groups rather than plot them over time.
export interface GroupTotal extends ConvertedMoney {
  group: string
  currency: string
  cost: number
  input_tokens: number
  output_tokens: number
  requests: number
  unpriced_input_tokens: number
  unpriced_output_tokens: number
}

// One mission's total ledger footprint. unpriced_requests counts turns
// whose cost is unknown (NULL in the ledger) — cost_by_currency is
// then a floor per currency, not the whole bill.
export interface ModelUsed {
  provider: string
  model: string
  requests: number
  last_used: string
}

export interface MissionUsage {
  mission_id: string
  cost_by_currency: Record<string, number>
  // converted_cost_by_currency mirrors cost_by_currency, converted into
  // default_currency, present only when at least one entry had a
  // usable stored fx rate — an entry with no rate is simply omitted,
  // so this map's total can be a floor, not the whole bill.
  converted_cost_by_currency?: Record<string, number>
  rate_as_of?: string
  input_tokens: number
  output_tokens: number
  requests: number
  unpriced_requests: number
  models: ModelUsed[]
}

export interface SessionUsage extends ConvertedMoney {
  session_id: string
  currency: string
  cost: number
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

// Budget position per UTC calendar window; limit is null when no
// budget is configured, in which case currency/spend are also absent
// (no currency to scope spend to yet).
export interface BudgetLimit extends ConvertedMoney {
  amount: number
  currency: string
}

export interface BudgetWindow extends ConvertedMoney {
  currency: string
  limit: BudgetLimit | null
  spend: number
  over: boolean
}

export interface BudgetStatus {
  day: BudgetWindow
  month: BudgetWindow
}

// Control-plane shapes (/v1/admin/*). credential_ref is a NAME the
// secret is stored under (in whichever backend is default) — secret
// values never travel.
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
  options?: {
    reasoning_effort?: string
    request_timeout?: string
    region?: string
    anthropic_base_url?: string
  }
}

export interface ChainEntry {
  provider_id: string
  model: string
  // harness names a delegated mission executor this entry dispatches
  // to instead of serving chat directly (D-051) — "" (or absent) is
  // the native API-serving axis, "claude-cli" the only known harness
  // so far. See router.KnownHarnesses.
  harness?: string
}

// RouteEntryStatus is the router's live view of one chain entry: the
// usability gate verdict plus the ledger stats and normalized factors
// behind scored strategies. Numeric fields are absent when the ledger
// has no data (or the model is unpriced) — never a guessed 0.
export interface RouteEntryStatus {
  provider_id: string
  provider_name?: string
  // provider_kind is 'api' | 'cli' — 'cli' rows are mission-only
  // executor providers (D-051), never built into a chat client.
  provider_kind?: string
  harness?: string
  model: string
  usable: boolean
  skip_reason?: string
  score?: number
  norm_price?: number
  norm_latency?: number
  norm_tps?: number
  uptime?: number
  latency_ms?: number
  tokens_per_s?: number
  output_per_mtok?: number
}

export interface AdminRoute {
  name: string
  chain: ChainEntry[]
  // 'ordered' tries the chain as written; 'auto' | 'price' | 'latency'
  // score entries from recent ledger stats and declared prices.
  strategy: string
  enabled: boolean
  // What this route can serve: 'chat' | 'embeddings' | 'vision'.
  capability?: string
  // Set when this route serves one of the 4 roles Timothy requires to
  // work: 'default' | 'embedding' | 'vision' | 'summarize'. Absent for
  // a plain, user-owned route.
  role?: string
  // Router try order with live stats — present for enabled routes once
  // a snapshot is loaded. serving is the first usable resolved entry.
  resolved?: RouteEntryStatus[]
  serving?: ChainEntry
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
  // Mission-only fields (internal/brain/missions) — meaningless to a
  // chat-only agent, absent or at zero values for one. Optional here
  // since most call sites (chat agent picker etc.) never populate
  // them.
  review_route?: string
  budget_usd?: number
  approval_allowlist?: string[]
}

// AdminTool is one entry of the live tool surface (builtins +
// connector tools) — feeds the agent editor's tools allowlist picker
// so a name is chosen from what actually exists, never typed blind.
export interface AdminTool {
  name: string
  description: string
}

// PlanUnit is one item of a mission's plan. passes is flipped only by
// the harness (RunVerify), never claimed by the model.
export interface PlanUnit {
  title: string
  verify_cmd: string
  artifacts?: string[]
  passes: boolean
}

export interface ProgressNote {
  at: string
  note: string
}

// Mission is one long-running, agent-driven unit of work
// (internal/brain/missions): explore -> plan -> execute -> review
// under a state machine.
export interface Mission {
  id: string
  goal: string
  kind: 'coding' | 'general'
  agent_id?: string
  phase: 'explore' | 'plan' | 'execute' | 'review' | 'done' | 'failed'
  status: 'idle' | 'working' | 'waiting_for_input' | 'paused' | 'done' | 'error'
  pause_reason?: 'backoff' | 'no_progress' | 'infra' | 'budget' | 'mixed_currency' | ''
  pause_message?: string
  workspace?: string
  worktree?: string
  branch?: string
  base_commit?: string
  // explore_notes is set once, at the end of the explore phase
  // (driver.go's runExplore) — absent/empty for a mission created
  // before the explore phase existed, or one that hasn't reached it
  // yet.
  explore_notes?: string
  spec: { units: PlanUnit[] }
  progress: ProgressNote[]
  iteration: number
  max_iterations: number
  consecutive_failures: number
  last_gap_fingerprint?: string
  stall_count: number
  budget_amount?: number
  budget_currency?: string
  route: string
  review_route: string
  escalation_route?: string
  pending_permission?: string
  pending_permission_tool?: string
  pending_permission_args?: string
  pending_permission_danger?: string
  pending_permission_rationale?: string
  last_evidence?: string
  auto_approve_safe: boolean
  schedule_id?: string
  created_at: string
  updated_at: string
}

export interface MissionEvent {
  mission_id: string
  seq: number
  kind: string
  payload: unknown
  provenance: string
  fingerprint?: string
  created_at: string
}

// ExecutorUsage is executor.result's token/cost usage block. cost_usd
// is null when the run authenticated via a subscription or oauth_token
// (no per-call price) rather than metered API billing — never a
// guessed 0 (D-013).
export interface ExecutorUsage {
  input_tokens: number
  output_tokens: number
  cache_read?: number
  cache_write?: number
  cost_usd?: number | null
}

// Payloads for the delegated coding-CLI executor's mission_events
// (D-051, brain's missions harness). Timeline rendering keys off
// event.kind, these types just name the shapes for that rendering.
export interface ExecutorSpawnedPayload {
  harness: string
  provider: string
  model: string
  auth_mode: string
  run_id: string
}

export interface ExecutorProgressPayload {
  run_id: string
  byte_offset: number
  turns: number
  tool_calls: number
}

export interface ExecutorResultPayload {
  status: string
  is_error: boolean
  duration_ms: number
  exit_code: number
  parse: string
  denials: string[]
  usage: ExecutorUsage
}

export interface ExecutorDiedPayload {
  reason: string
  exit_code?: number
  stderr_tail?: string
}

export interface ExecutorIdleKilledPayload {
  idle_s: number
}

export interface ExecutorAuthFailedPayload {
  harness: string
}

// MissionFile is one entry of a mission workspace's file listing
// (GET /v1/missions/:id/files). Declared marks files named in the
// mission's plan artifacts, not the full tree.
export interface MissionFile {
  path: string
  size: number
  mtime: string
  declared: boolean
}

export interface Notification {
  id: string
  mission_id: string
  kind: string
  message: string
  read: boolean
  created_at: string
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
  // sensitive pins every tool this connector serves onto the
  // privacy-floor route (session.SensitiveTools), same as gmail_read
  // is pinned today — additive, connector-wide, no code change needed.
  sensitive: boolean
}

// MissionTemplate is the frozen mission-creation payload a schedule
// fires at each tick (internal/brain/missions.MissionTemplate) — same
// shape as CreateMissionInput.
export interface MissionTemplate {
  goal: string
  kind: 'coding' | 'general'
  agent_id?: string
  route?: string
  review_route?: string
  max_iterations?: number
  budget_amount?: number
  budget_currency?: string
  auto_approve_safe?: boolean
}

// Schedule is a recurring cron trigger that fires mission_template
// (internal/brain/missions.Schedule), managed from the Missions page's
// recurring schedules section. next_run is server-computed, present
// whenever the cron parses.
export interface Schedule {
  id: string
  name: string
  cron: string
  mission_template: MissionTemplate
  enabled: boolean
  expires_at?: string
  last_run?: string
  next_run?: string
  created_at: string
  updated_at: string
  pending_fire: boolean
  last_skipped_at?: string
  skip_reason?: string
}
