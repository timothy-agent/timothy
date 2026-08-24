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
  // knowledge is the set of kb collection names pinned for this turn;
  // the server unions them into the session's knowledge list.
  knowledge?: string[]
}

// --- session management (mirrors brain's /v1/sessions surface) ---

export interface SessionMeta {
  id: string
  title: string
  archived: boolean
  agent?: string
  last_route?: string
  // knowledge is the session's pinned kb collection names.
  knowledge?: string[]
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
  // unbilled_cost is the metered-price equivalent of spend billed
  // through a subscription/oauth_token executor (D-051) — real spend
  // was $0, excluded from cost, never folded into it.
  unbilled_cost: number
  // converted_unbilled_cost mirrors converted_amount, same mechanism
  // (usage.go's decorator), for unbilled_cost specifically — present
  // only when a stored fx rate exists and unbilled_cost is nonzero.
  converted_unbilled_cost?: number
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
  // unbilled_cost mirrors UsageSummary's field — excluded from cost.
  unbilled_cost: number
  // converted_unbilled_cost mirrors UsageSummary's field.
  converted_unbilled_cost?: number
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

// UnpricedGroup is one (provider, model) pair's unpriced-token totals
// over a range (GET /v1/admin/usage/unpriced) — grouped by provider
// alongside model so the catalog estimate (catalogPrices) can resolve
// each pair against that provider's own catalog candidates only.
export interface UnpricedGroup {
  provider: string
  model: string
  unpriced_input_tokens: number
  unpriced_output_tokens: number
}

// One mission's total ledger footprint. unpriced_requests counts turns
// whose cost is unknown (NULL in the ledger) — cost_by_currency is
// then a floor per currency, not the whole bill.
export interface ModelUsed {
  provider: string
  model: string
  // harness is true when this row is the delegated CLI executor's own
  // calls (purpose='executor', D-051) rather than brain's direct ones —
  // a model used by both sides yields two separate rows.
  harness: boolean
  requests: number
  last_used: string
}

export interface MissionUsage {
  mission_id: string
  // cost_by_currency is billed spend only — unbilled (subscription-
  // billed) rows are excluded, so this is the mission's true bill.
  // Equals billed_brain_by_currency + billed_harness_by_currency.
  cost_by_currency: Record<string, number>
  // converted_cost_by_currency mirrors cost_by_currency, converted into
  // default_currency, present only when at least one entry had a
  // usable stored fx rate — an entry with no rate is simply omitted,
  // so this map's total can be a floor, not the whole bill.
  converted_cost_by_currency?: Record<string, number>
  // billed_brain_by_currency/billed_harness_by_currency split billed
  // spend by who incurred it: the delegated CLI executor's own rows
  // (harness, D-051) vs everything else the missions engine billed
  // directly (brain: explore/plan/worker/review).
  billed_brain_by_currency?: Record<string, number>
  billed_harness_by_currency?: Record<string, number>
  // unbilled_cost_by_currency is the API-equivalent price of rows
  // billed through a subscription/oauth_token executor (D-051) —
  // real spend was $0, this is what the same work would have cost
  // metered.
  unbilled_cost_by_currency?: Record<string, number>
  converted_unbilled_cost_by_currency?: Record<string, number>
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

// CatalogModel is one model_catalog row (GET /v1/admin/catalog/models)
// — LiteLLM's synced pricing/context data. Prices are per MILLION
// tokens (Timothy's convention); absent means unknown, never guessed.
// id is the id the provider's own API actually accepts: model_key with
// LiteLLM's namespacing provider prefix stripped server-side
// (gateway/catalog.StripOwnPrefix) — model_key is kept alongside for
// reference/debug, but a picker must display and commit id, never
// model_key.
export interface CatalogModel {
  id: string
  model_key: string
  litellm_provider: string
  mode: string
  max_input_tokens?: number
  max_output_tokens?: number
  input_per_mtok?: number
  output_per_mtok?: number
  cache_read_per_mtok?: number
  cache_write_per_mtok?: number
}

// CatalogPriceQuery is one (provider, model) pair POST
// /v1/admin/catalog/prices resolves — provider is a providers row's
// name exactly as recorded in cost_ledger.provider (UnpricedGroup's own
// field), so a caller reading unpriced usage can pass its rows straight
// through.
export interface CatalogPriceQuery {
  provider: string
  model: string
}

// CatalogPrice is one requested (provider, model) pair's resolved
// catalog price from POST /v1/admin/catalog/prices — provider/model
// echo the request pair back; price is null when the provider name is
// unknown or the model has no match within that provider's catalog
// candidates (never matched against another vendor's catalog rows).
export interface CatalogPrice extends CatalogPriceQuery {
  price: Pick<
    CatalogModel,
    'input_per_mtok' | 'output_per_mtok' | 'cache_read_per_mtok' | 'cache_write_per_mtok'
  > | null
}

// CatalogSyncStatus mirrors the model_catalog_sync singleton row (GET
// /v1/admin/catalog/status, POST .../refresh).
export interface CatalogSyncStatus {
  fetched_at: string | null
  entry_count: number
  error: string
}

export interface AdminProvider {
  id: string
  name: string
  kind: string
  driver: string
  base_url: string
  default_model: string
  credential_ref: string
  headers: Record<string, string>
  enabled: boolean
  options?: {
    reasoning_effort?: string
    request_timeout?: string
    region?: string
    anthropic_base_url?: string
    litellm_provider?: string
  }
}

export interface ChainEntry {
  provider_id: string
  model: string
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
  input_per_mtok?: number
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
  // responses_ok: whether the endpoint serves POST /responses (the
  // OpenAI Responses API codex-cli requires) — absent when unprobed or
  // the probe outcome was ambiguous, never affects ok.
  responses_ok?: boolean
}

// AvailableModel is one model reported by a provider's own listing
// endpoint (GET /v1/admin/providers/:id/models). display_name is set
// only by drivers whose listing endpoint reports one (cursor-cli).
export interface AvailableModel {
  id: string
  display_name?: string
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
  // Knowledge collections this agent can search with kb_search.
  // Optional: the backend doesn't send it yet, so callers default to
  // [] when absent.
  knowledge?: string[]
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

// AdminSkill is one loaded skill pack — feeds the agent editor's
// skills allowlist picker so a name is chosen from what actually
// exists, never typed blind.
export interface AdminSkill {
  name: string
  description: string
}

// KbCollection is one document collection agents can search with
// kb_search — a named group of ingested documents.
export interface KbCollection {
  id: string
  name: string
  description: string
  doc_count: number
  chunk_count: number
  // failed_count is how many documents in this collection are in the
  // 'failed' ingestion state.
  failed_count: number
  created_at: string
  updated_at: string
}

// KbDocument is one ingested file within a collection. status tracks
// the ingestion pipeline: pending (queued) -> ingesting (chunking +
// embedding) -> ready | failed.
export interface KbDocument {
  id: string
  collection_id: string
  title: string
  source_type: 'file' | 'notion' | 'wiki' | 'url'
  source_ref: string
  status: 'pending' | 'ingesting' | 'ready' | 'failed'
  error: string
  chunk_count: number
  bytes: number
  ingested_at: string | null
  created_at: string
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
  // name is a short display name generated once from goal, the same
  // way a chat session's title is — empty until generation lands (or
  // for a mission predating this field); the UI falls back to a
  // truncated goal.
  name?: string
  kind: 'coding' | 'general'
  agent_id?: string
  phase: 'explore' | 'plan' | 'execute' | 'review' | 'done' | 'failed'
  status: 'idle' | 'working' | 'waiting_for_input' | 'paused' | 'done' | 'error'
  // failure_reason is derived server-side (Store.List/Get) from this
  // mission's latest mission.failed event's payload.reason —
  // "cancelled" or "max_iterations" — set only when phase is 'failed'.
  failure_reason?: string
  pause_reason?: 'backoff' | 'no_progress' | 'infra' | 'budget' | 'mixed_currency' | ''
  pause_message?: string
  workspace?: string
  worktree?: string
  branch?: string
  base_commit?: string
  // repo_url is the GitHub repo this coding mission was cloned from
  // (https clone URL) — absent means the self-init'd empty repo.
  // connector_id names the github-kind connector whose PAT
  // authenticated the clone; only present alongside repo_url.
  repo_url?: string
  connector_id?: string
  // on_complete is the operator's consent-at-create choice for what the
  // harness does automatically when this mission reaches done: '' does
  // nothing, 'push' pushes the branch, 'push_pr' pushes then opens a
  // pull request. Only ever set at create time, never by the model.
  on_complete?: '' | 'push' | 'push_pr'
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
  // plan_route, when set, is the route explore/plan/replan/review run
  // on instead of route — "" means route covers everything.
  plan_route?: string
  escalation_route?: string
  pending_permission?: string
  pending_permission_tool?: string
  pending_permission_args?: string
  pending_permission_danger?: string
  pending_permission_rationale?: string
  last_evidence?: string
  auto_approve_safe: boolean
  // environment is the sandbox image key (D-05x) this coding mission's
  // container runs — "" means base, resolved server-side at create
  // time (explicit request > repo markers > goal keyword > base).
  // General missions never set this.
  environment?: string
  // harness is the delegated CLI executor this coding mission's worker
  // turns run under (D-051): "" or absent is native in-process
  // dispatch, "claude-cli"/"pi"/"codex-cli"/"opencode"/"cursor-cli" name
  // a registered executor.
  harness?: string
  // branch_pattern/commit_style are this mission's own override of the
  // settings-configured git strategy defaults; "" or absent means the
  // settings default applied at provisioning/commit time.
  branch_pattern?: string
  commit_style?: string
  // top_model/top_model_provider are decorated onto the list/get
  // response from the cost ledger's top-served-model-per-mission
  // lookup (internal/brain/api/missions.go's decorateTopModels) — the
  // model that actually served this mission, by request count. Absent
  // for a mission with no ledger rows yet.
  top_model?: string
  top_model_provider?: string
  schedule_id?: string
  // parent_mission_id names the terminal mission this one follows up
  // on — absent for an ordinary mission. parent_context is that
  // parent's outcome digest, snapshotted at follow-up create time.
  parent_mission_id?: string
  parent_context?: string
  // attachments are PDF documents attached at create time — markdown is
  // never sent over the wire (see api/missions.go's sanitizeMission).
  attachments?: { id: string; mime: string; name?: string }[]
  // destination_ids names operator-created destinations (email,
  // webhook) this mission delivers its outcome digest to on the
  // terminal done transition. Validated against the destinations table
  // at create time — never model-decided.
  destination_ids?: string[]
  // light marks a mission that skips explore/plan/review (D-069):
  // kind=general only, born in phase=execute, one bare worker turn.
  // final_output is that worker's verbatim final message — the
  // deliverable itself, absent/empty until the mission reaches done.
  // Invariant: final_output is only ever populated when light is true;
  // a non-light mission's Result comes from last_evidence instead (see
  // MissionDetail.tsx's mutually exclusive light/!light Result blocks).
  light?: boolean
  final_output?: string
  created_at: string
  updated_at: string
}

// Destination is one operator-created outbound sink for mission
// results (internal/brain/destinations.Destination) — Settings →
// Destinations CRUD, and the mission form's destinations multi-select.
export interface Destination {
  id: string
  name: string
  kind: 'email' | 'webhook' | 'telegram'
  config: Record<string, unknown>
  credential_ref: string
  enabled: boolean
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
// guessed 0 (D-013). cost_usd_billed is true only when cost_usd is the
// SAME figure the cost ledger booked as real spend (Anthropic
// first-party api_key) — false whenever cost_usd is merely the CLI's
// own harness-reported figure: subscription/oauth_token (never billed)
// or a non-anthropic provider (priced against Anthropic's table, which
// is fiction for that provider — the ledger prices it separately from
// that provider's own rows, or leaves it unpriced).
export interface ExecutorUsage {
  input_tokens: number
  output_tokens: number
  cache_read?: number
  cache_write?: number
  cost_usd?: number | null
  cost_usd_billed?: boolean
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

export interface ExecutorSkippedPayload {
  harness: string
  reason: 'unknown_harness' | 'resolve_failed' | 'no_usable_entry' | 'cooldown'
  error?: string
  until?: string
  provider?: string
  model?: string
  skip_reasons?: string[]
}

// MissionSteeredPayload is mission.steered's payload — operator
// guidance injected into a running mission via POST .../note.
export interface MissionSteeredPayload {
  note: string
}

// MissionPROpenedPayload is mission.pr_opened's payload — recorded by
// POST .../pr once a pull request is opened (or an existing one for
// the same head is found instead).
export interface MissionPROpenedPayload {
  url: string
  number: number
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
// tools (MCP server or Google account), or — for kind 'github' — an
// identity/credential connector with no tools of its own (mission
// flows and Settings resolve a GitHub identity from its PAT; chat
// tools stay on the MCP-based GitHub connector). config is
// kind-specific; the credential_ref names where its secret/tokens live.
export interface AdminConnector {
  id: string
  name: string
  kind: 'mcp' | 'google' | 'github'
  config: Record<string, unknown>
  credential_ref: string
  enabled: boolean
  // sensitive pins every tool this connector serves onto the
  // privacy-floor route (session.SensitiveTools), same as gmail_read
  // is pinned today — additive, connector-wide, no code change needed.
  sensitive: boolean
}

// GitHubIdentity is what a github-kind connector's test resolves:
// which account its PAT authenticates as.
export interface GitHubIdentity {
  login: string
  name: string
  email: string
  scopes: string
}

// GitHubRepo is one repo a github-kind connector's PAT can see or
// create (GET/POST /v1/admin/connectors/:id/repos) — the mission
// create form's repo picker/create-new flow.
export interface GitHubRepo {
  full_name: string
  private: boolean
  default_branch: string
  html_url: string
  clone_url: string
  pushed_at: string
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
  // plan_route, when set, is the route explore/plan/replan/review run
  // on instead of route — "" means route covers everything.
  plan_route?: string
  max_iterations?: number
  budget_amount?: number
  budget_currency?: string
  auto_approve_safe?: boolean
  harness?: string
  environment?: string
  branch_pattern?: string
  commit_style?: string
  // destination_ids names operator-created destinations this template's
  // fired missions deliver their outcome digest to. Re-validated at
  // fire time — a destination deleted or disabled since the schedule
  // was created is dropped silently rather than failing the fire.
  destination_ids?: string[]
  // light marks a mission that skips explore/plan/review (D-069);
  // only valid for kind=general, rejected for kind=coding at schedule
  // create/update.
  light?: boolean
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
