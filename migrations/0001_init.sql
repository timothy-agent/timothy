CREATE TABLE IF NOT EXISTS schema_migrations (
    version    text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS providers (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name           text UNIQUE NOT NULL,
    kind           text NOT NULL CHECK (kind IN ('api', 'cli')),
    driver         text NOT NULL,
    base_url       text NOT NULL DEFAULT '',
    default_model  text NOT NULL DEFAULT '',
    credential_ref text NOT NULL DEFAULT '',
    headers        jsonb NOT NULL DEFAULT '{}',
    options        jsonb NOT NULL DEFAULT '{}',
    enabled        boolean NOT NULL DEFAULT false,
    -- D-033 follow-up: opts a provider out of BootstrapChain's
    -- auto-fallback fill, so a local/dev provider (e.g. Ollama) doesn't
    -- get silently appended as a fallback on shared routes.
    exclude_from_bootstrap boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Routes are named provider/model fallback chains (D-033): the
-- routing primitive. Agents reference a route by name; system callers
-- (embedding, summarize) use fixed names. strategy picks the chain
-- order: 'ordered' keeps the written priority, 'auto' scores entries
-- from recent ledger stats and declared prices, 'price'/'latency' are
-- auto with that factor dominant.
--
-- Routing is fully dynamic (no hardcoded route names in code):
-- capability is what a route can serve (chat/embeddings/vision,
-- replacing a name=="embedding" special case in
-- internal/gateway/router/router.go); role marks which route serves
-- one of the 4 functions Timothy requires to work at all
-- (default/embedding/vision/summarize). A route's name is free text
-- the user can rename at will: code resolves "the route with
-- role = X", never a literal name.
CREATE TABLE IF NOT EXISTS routes (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text UNIQUE NOT NULL,
    chain      jsonb NOT NULL DEFAULT '[]',
    strategy   text NOT NULL DEFAULT 'ordered'
        CHECK (strategy IN ('ordered', 'auto', 'price', 'latency')),
    enabled    boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    capability text NOT NULL DEFAULT 'chat'
        CHECK (capability IN ('chat', 'embeddings', 'vision')),
    role       text CHECK (role IN ('default', 'embedding', 'vision', 'summarize'))
);

CREATE UNIQUE INDEX IF NOT EXISTS routes_role_unique_idx ON routes (role) WHERE role IS NOT NULL;

CREATE TABLE IF NOT EXISTS cost_ledger (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    ts                 timestamptz NOT NULL DEFAULT now(),
    provider           text NOT NULL,
    model              text NOT NULL,
    route              text NOT NULL,
    agent              text NOT NULL DEFAULT '',
    session_id         text,
    mission_id         text,
    input_tokens       integer,
    output_tokens      integer,
    cache_read_tokens  integer,
    cache_write_tokens integer,
    -- Subset of output_tokens already included and billed there (OpenAI
    -- reasoning models); stored separately for spend visibility only.
    reasoning_tokens   integer,
    latency_ms         integer NOT NULL,
    status             text NOT NULL,
    error_code         text,
    cost               numeric(12, 6),
    -- Provider's billing currency (D-013's cost-honesty sibling: no FX
    -- conversion happens anywhere in this codebase, so this column
    -- must always match the currency the provider actually billed in).
    -- All current providers bill USD; the column exists so a future
    -- non-USD provider doesn't silently get mislabeled as USD.
    currency           char(3) NOT NULL DEFAULT 'USD',
    -- Distinguish WHY a call was made from WHICH route served it:
    -- distill, auto-title, and compaction summaries all ride cheap
    -- routes, so the route alone cannot separate internal calls from
    -- user chat.
    purpose            text,
    -- True when cost is the CLI-reported API-equivalent price for a
    -- subscription/oauth-billed delegated executor run: a real figure,
    -- but not actual marginal spend (D-013 amended by operator
    -- decision, tracked so subscription runs aren't budget-invisible).
    unbilled           boolean NOT NULL DEFAULT false,
    -- Provider's own id for this request (OpenAI resp_.../chatcmpl-...,
    -- Anthropic msg_...), distinct from id above: lets a row be
    -- reconciled against the provider's own usage export.
    provider_request_id text
);

CREATE INDEX IF NOT EXISTS cost_ledger_ts_idx ON cost_ledger (ts);
CREATE INDEX IF NOT EXISTS cost_ledger_session_idx ON cost_ledger (session_id) WHERE session_id IS NOT NULL;
-- Dashboard aggregation reads the ledger by time range then groups by
-- provider/model/route; this composite index covers that scan.
CREATE INDEX IF NOT EXISTS cost_ledger_ts_dims_idx
    ON cost_ledger (ts, provider, model, route);
-- Mission spend rollups (missions.Store.Spend) filter by mission_id;
-- mirrors cost_ledger_session_idx's partial-index shape.
CREATE INDEX IF NOT EXISTS cost_ledger_mission_idx ON cost_ledger (mission_id) WHERE mission_id IS NOT NULL;

-- Every control-plane mutation leaves a row: what changed, from what,
-- to what. Single-token system, so no actor column yet (the bearer IS
-- the admin).
CREATE TABLE IF NOT EXISTS admin_audit (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    ts        timestamptz NOT NULL DEFAULT now(),
    action    text NOT NULL,
    entity    text NOT NULL,
    entity_id text NOT NULL,
    before    jsonb,
    after     jsonb
);

CREATE INDEX IF NOT EXISTS admin_audit_ts_idx ON admin_audit (ts);

-- Daily USD-base reference rates for DISPLAY conversion only (D-013's
-- honesty invariant is unchanged: cost_ledger.cost/currency are never
-- rewritten, and nothing here feeds back into what a provider was
-- actually billed). Append-only by day: a (base, quote, as_of) row is
-- never updated once written, so a rate a UI already showed stays
-- reproducible. base is always 'USD' today (internal/brain/fxrates'
-- fetcher's only source), kept as a column rather than assumed so a
-- future non-USD-base source doesn't need a schema change.
CREATE TABLE IF NOT EXISTS fx_rates (
    base       char(3) NOT NULL,
    quote      char(3) NOT NULL,
    rate       numeric NOT NULL,
    as_of      date NOT NULL,
    fetched_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (base, quote, as_of)
);

-- Seed only the route NAMES the code references, no providers, no
-- chains, no opinions (the user configures providers in Settings and
-- fills these chains there). Fixed roles and their consumers:
--   role 'default'   : chat's fallback route (internal/brain/chat)
--   role 'summarize' : session compactor (internal/brain/session)
--   role 'embedding' : gateway /v1/embeddings (internal/gateway/api)
--   role 'vision'    : image messages auto-flip here (D-046,
--                       internal/brain/chat); gateway falls back to
--                       the default role when this route is
--                       missing/disabled/empty
-- All start with an empty chain and disabled. Guarded both ways: skip
-- if a row with this name exists OR a row already holds this role (a
-- user may have renamed a role-bearing route; a bare ON CONFLICT
-- (name) would then violate routes_role_unique_idx).
INSERT INTO routes (name, capability, role)
SELECT 'default', 'chat', 'default'
WHERE NOT EXISTS (SELECT 1 FROM routes WHERE name = 'default' OR role = 'default');

INSERT INTO routes (name, capability, role)
SELECT 'summarize', 'chat', 'summarize'
WHERE NOT EXISTS (SELECT 1 FROM routes WHERE name = 'summarize' OR role = 'summarize');

INSERT INTO routes (name, capability, role)
SELECT 'embedding', 'embeddings', 'embedding'
WHERE NOT EXISTS (SELECT 1 FROM routes WHERE name = 'embedding' OR role = 'embedding');

INSERT INTO routes (name, capability, role)
SELECT 'vision', 'vision', 'vision'
WHERE NOT EXISTS (SELECT 1 FROM routes WHERE name = 'vision' OR role = 'vision');

-- Event-sourced sessions: an append-only log per session with two
-- projections (UI transcript, LLM context) derived in code.
CREATE TABLE IF NOT EXISTS sessions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title      text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    archived   boolean NOT NULL DEFAULT false,
    last_route text NOT NULL DEFAULT '',
    -- Session-scoped knowledge: kb_collection names the user pinned to
    -- this session (composer # mentions); unioned with the serving
    -- agent's own knowledge list per turn.
    knowledge  jsonb NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS session_events (
    session_id uuid NOT NULL REFERENCES sessions(id),
    seq        bigint NOT NULL,
    kind       text NOT NULL,
    payload    jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, seq)
);

-- Full-text search over user messages (session search in the API).
CREATE INDEX IF NOT EXISTS session_events_user_text_idx
    ON session_events
    USING gin (to_tsvector('english', payload->>'text'))
    WHERE kind = 'user_message';

CREATE INDEX IF NOT EXISTS sessions_updated_idx ON sessions (updated_at DESC);

-- Tool execution audit trail and offloaded outputs. Every tool call
-- writes an audit row; results too large for the model's context are
-- stored here in full and referenced by id (retrieved on demand via
-- the retrieve_output tool, garbage-collected after a retention
-- window).

CREATE TABLE IF NOT EXISTS tool_audit (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    ts          timestamptz NOT NULL DEFAULT now(),
    session_id  uuid NOT NULL,
    tool        text NOT NULL,
    args_digest text NOT NULL,
    status      text NOT NULL,
    duration_ms bigint NOT NULL,
    error       text
);

CREATE INDEX IF NOT EXISTS tool_audit_session_idx
    ON tool_audit (session_id, ts);

CREATE TABLE IF NOT EXISTS tool_outputs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id uuid NOT NULL,
    tool       text NOT NULL,
    content    text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS tool_outputs_created_idx
    ON tool_outputs (created_at);

-- Permission chain state (D-010). project_allowlist holds standing
-- grants (panel-editable later); session_grants holds "allow for this
-- session" answers with an expiry. Patterns are globs matched against
-- the call's subject (shell command, fetched URL); the hard policy
-- guard and the danger classifier live in code, not here.

CREATE TABLE IF NOT EXISTS project_allowlist (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tool       text NOT NULL,
    pattern    text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tool, pattern)
);

CREATE TABLE IF NOT EXISTS session_grants (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id uuid NOT NULL,
    tool       text NOT NULL,
    pattern    text NOT NULL,
    expires    timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS session_grants_session_idx
    ON session_grants (session_id, tool);

-- Long-term memory: typed, staged, supersede-only (D-011). Facts are
-- never UPDATEd in place: corrections insert a new row and point the
-- old row's superseded_by at it. Retrieval only ever sees
-- status='active'. Entities stay a plain relational table; no graph
-- database.

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS memories (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type              text NOT NULL CHECK (type IN ('episodic', 'semantic', 'procedural')),
    content           text NOT NULL,
    embedding         vector(1024),
    entity_refs       uuid[] NOT NULL DEFAULT '{}',
    source_session    uuid,
    source_seq        bigint,
    actor             text NOT NULL DEFAULT 'agent',
    created_at        timestamptz NOT NULL DEFAULT now(),
    last_confirmed_at timestamptz NOT NULL DEFAULT now(),
    superseded_by     uuid REFERENCES memories (id),
    status            text NOT NULL CHECK (status IN ('pending', 'active', 'rejected', 'archived')),
    confidence        real,
    tsv               tsvector GENERATED ALWAYS AS (to_tsvector('english', content)) STORED,
    -- Retrieval bookkeeping for the consolidation job (archive episodic
    -- memories unretrieved for 180d), this is NOT last_confirmed_at,
    -- which only explicit confirmation or re-extraction may bump
    -- (D-011).
    last_retrieved_at timestamptz,
    -- Usage-driven decay bookkeeping (memory-extraction-v2 slice 5):
    -- how many times retrieval has returned this memory. Metadata
    -- bookkeeping, not a fact UPDATE (D-011); memory content stays
    -- supersede-only.
    retrieval_hits    integer NOT NULL DEFAULT 0
);

-- m/ef_construction over pgvector defaults (16/64): better recall at
-- this corpus scale, negligible build cost.
CREATE INDEX IF NOT EXISTS memories_embedding_hnsw
    ON memories USING hnsw (embedding vector_cosine_ops)
    WITH (m = 20, ef_construction = 200);

CREATE INDEX IF NOT EXISTS memories_tsv_gin ON memories USING gin (tsv);

CREATE INDEX IF NOT EXISTS memories_status_type_idx ON memories (status, type);

CREATE TABLE IF NOT EXISTS entities (
    id   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type text NOT NULL CHECK (type IN
        ('person', 'project', 'service', 'preference', 'decision', 'topic', 'place')),
    name text NOT NULL,
    UNIQUE (type, name)
);

-- Brain-owned runtime settings (D-032): boolean feature switches and
-- typed string knobs in one table, value as jsonb. Absent key = the
-- built-in default (switches enabled, knobs empty): defaults live in
-- code so a fresh database behaves like before this table existed.
CREATE TABLE IF NOT EXISTS settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Spend budgets for the alert surface: at most one limit per window,
-- scoped to a single currency (no FX conversion anywhere in this
-- codebase, spend is only ever compared within the same currency).
-- Absent row = no budget set, nothing to alert on.
CREATE TABLE IF NOT EXISTS spend_budgets (
    scope         text PRIMARY KEY CHECK (scope IN ('day', 'month')),
    limit_amount  numeric(12, 2) NOT NULL CHECK (limit_amount > 0),
    currency      char(3) NOT NULL DEFAULT 'USD',
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Secret values referenced by providers.credential_ref (and future
-- connectors). A ref name resolves only here, there is no env var
-- fallback; an unresolved ref leaves its provider keyless/unhealthy.
-- backend='db' secrets are envelope-encrypted with TIMOTHY_MASTER_KEY;
-- backend='vault'/'asm' rows carry no ciphertext, only backend_ref (a
-- Timothy-owned path/id under timothy/<ref_name>, written by the store
-- itself), the value is fetched from that system at read time.
CREATE TABLE IF NOT EXISTS secrets (
    ref_name    text PRIMARY KEY,
    backend     text NOT NULL DEFAULT 'db',
    ciphertext  bytea,
    nonce       bytea,
    backend_ref text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT secrets_backend_check CHECK (backend IN ('db', 'vault', 'asm')),
    CONSTRAINT secrets_db_has_ciphertext
        CHECK (backend <> 'db' OR (ciphertext IS NOT NULL AND nonce IS NOT NULL)),
    CONSTRAINT secrets_external_has_ref
        CHECK (backend = 'db' OR backend_ref <> '')
);

-- Connection settings for secret backends, editable from the Settings
-- UI. Non-secret parts only: the Vault token itself lives in the
-- secrets table (backend 'db') under the ref named by the config's
-- token_ref, so this table never holds a credential.
--
-- Exactly one backend is the store-wide default (D-031): every
-- credential entered in the UI is stored through it. 'db' (built-in
-- storage) needs no config but sits in the table so it can carry the
-- flag; it is seeded as the default.
CREATE TABLE IF NOT EXISTS secret_backend_config (
    backend    text PRIMARY KEY,
    config     jsonb NOT NULL DEFAULT '{}'::jsonb,
    is_default boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT secret_backend_config_check CHECK (backend IN ('db', 'vault', 'asm'))
);

-- At most one default, enforced by the database not the application.
CREATE UNIQUE INDEX IF NOT EXISTS secret_backend_config_one_default
    ON secret_backend_config ((true)) WHERE is_default;

INSERT INTO secret_backend_config (backend, config, is_default)
SELECT 'db', '{}'::jsonb, NOT EXISTS (SELECT 1 FROM secret_backend_config WHERE is_default)
ON CONFLICT (backend) DO NOTHING;

-- Connectors are third-party integrations the agent can call as tools
-- (MCP servers, Google Workspace, ...). Like providers they are data:
-- admin CRUD writes rows, brain reloads and surfaces each connector's
-- tools into the registry namespaced by connector name. credential_ref
-- names a secret in the secrets table, never a value.
CREATE TABLE IF NOT EXISTS connectors (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name           text UNIQUE NOT NULL,
    kind           text NOT NULL CHECK (kind IN ('mcp', 'google', 'github', 'microsoft', 'imap', 'caldav')),
    -- kind-specific settings: mcp → {transport, endpoint, headers},
    -- google/microsoft → {client_id, client_secret_ref, scopes},
    -- imap → {host, port, username, account_email, smtp_host,
    -- smtp_port} (port defaults 993/implicit TLS, smtp_port defaults
    -- 587/STARTTLS, smtp_host optional, SMTP send only when set).
    -- OAuth tokens NEVER live here; they go to the secrets table under
    -- credential_ref (imap's password goes there too).
    config         jsonb NOT NULL DEFAULT '{}',
    credential_ref text NOT NULL DEFAULT '',
    enabled        boolean NOT NULL DEFAULT false,
    -- Marks a whole connector as sensitive: every tool it serves
    -- shares the connector's name as a suffix (session.SensitiveTools),
    -- pinning them all onto the privacy-floor route without listing
    -- tool names in Go.
    sensitive      boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Agents are configuration, not code (D-030, D-034): a chat session
-- starts by choosing WHO serves it. An agent names a prompt overlay,
-- a route (its model chain), skill and tool allowlists (empty = none:
-- opt-in only, keeps an agent's tool schemas off the wire until named),
-- and whether long-term memory participates. Exactly one agent is the
-- default: the zero-click choice a new session gets.
CREATE TABLE IF NOT EXISTS agents (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name           text UNIQUE NOT NULL,
    description    text NOT NULL DEFAULT '',
    prompt_overlay text NOT NULL DEFAULT '',
    route          text NOT NULL DEFAULT '',
    skills         jsonb NOT NULL DEFAULT '[]',
    tools          jsonb NOT NULL DEFAULT '[]',
    memory         boolean NOT NULL DEFAULT true,
    is_default     boolean NOT NULL DEFAULT false,
    enabled        boolean NOT NULL DEFAULT true,
    -- Missions reuse the agents table (missions.agent_id -> agents.id)
    -- rather than a parallel agent_profiles table: a mission-serving
    -- agent is still "who serves this," same as chat. These columns
    -- are meaningless to a chat-only agent and stay at their defaults
    -- for one; a mission-capable agent sets what it needs. harness is
    -- the coding executor a mission delegates to (mission.harness ->
    -- agent.harness -> settings.coding_executor -> native); empty
    -- means inherit.
    review_route       text NOT NULL DEFAULT '',
    approval_allowlist jsonb NOT NULL DEFAULT '[]',
    harness            text NOT NULL DEFAULT '',
    -- Knowledge collections (kb_collections.name) this agent may search
    -- with search_kb (D-060): empty means none, same opt-in semantics
    -- as skills/tools: an agent must name a collection explicitly.
    knowledge      jsonb NOT NULL DEFAULT '[]',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS agents_one_default
    ON agents ((true)) WHERE is_default;

-- Seed exactly one agent: 'general', because exactly one default
-- agent must exist. Every other agent is created by the operator in
-- the UI. Route is 'default' (seeded above, guaranteed to exist by
-- table order). Skills/tools are
-- opt-in only (empty means none): the seed allowlists every shipped
-- skill pack (an allowlist entry costs one index line per turn, the
-- body loads only on demand) and a minimal tool surface, since every
-- listed tool schema rides every turn's prompt.
--
-- Tool names are the compiled-in builtins' exact registered names
-- (internal/brain/tools/builtin/*.go) plus the unified connector
-- capability names (connectors.Manager.Tools aggregates every
-- connected account behind one name per capability, e.g. "search_mail"
-- covers every connected google/microsoft mail account); an allowlist
-- entry here is agent-authored before any connector even exists, and
-- covers every current and future account serving that capability.
-- Guarded both ways for is_default: if any default agent already
-- exists, this seed must not attempt to set is_default=true.
INSERT INTO agents (name, description, prompt_overlay, route, skills, tools, is_default)
SELECT 'general', 'Everyday questions and tasks on a strong all-round chain.', '', 'default',
    '["research-brief", "deep-research", "coding", "email-research"]',
    '["get_current_time", "convert_time", "calculate", "convert_currency", "search_web", "fetch_url", "remember", "list_missions", "get_mission", "push_mission_branch", "followup_mission", "search_mail", "read_mail", "read_mail_attachment", "send_mail", "list_calendar_events", "create_calendar_event"]',
    NOT EXISTS (SELECT 1 FROM agents WHERE is_default)
WHERE NOT EXISTS (SELECT 1 FROM agents WHERE name = 'general');

-- Schedules fire mission templates on a cron cadence
-- (internal/brain/missions/scheduler.go). mission_template is applied
-- verbatim as the new mission's initial columns each firing.
CREATE TABLE IF NOT EXISTS schedules (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name              text UNIQUE NOT NULL,
    cron              text NOT NULL,
    mission_template  jsonb NOT NULL DEFAULT '{}',
    enabled           boolean NOT NULL DEFAULT true,
    expires_at        timestamptz,
    last_run          timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    -- A due fire skipped because a mission from this schedule was still
    -- active is not lost: pending_fire carries it forward so the next
    -- tick with no active mission fires it, instead of the schedule
    -- silently missing that boundary forever (scheduler.go's fireOne).
    pending_fire      boolean NOT NULL DEFAULT false,
    -- Records the most recent skip (backfill grace or active-mission
    -- dedup) for the schedules API to surface; cleared on any
    -- successful fire.
    last_skipped_at   timestamptz,
    skip_reason       text NOT NULL DEFAULT ''
);

-- Agentic workflows: an orchestration layer above missions
-- (internal/brain/workflows). A workflow is composable data (steps +
-- edges); missions stay atoms, spawned one at a time by the engine
-- reacting to mission terminal events. See
-- docs/2026-08-14-agentic-workflows-plan.md.
CREATE TABLE IF NOT EXISTS workflows (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text UNIQUE NOT NULL,
    definition  jsonb NOT NULL DEFAULT '{}',
    enabled     boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- status is deliberately NOT CHECK-constrained, same reasoning as
-- missions.phase/status below: a future code rollback that doesn't
-- recognize a newer status must degrade safely in Go, not have
-- Postgres reject the row.
CREATE TABLE IF NOT EXISTS workflow_runs (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_id   uuid NOT NULL REFERENCES workflows(id),
    status        text NOT NULL DEFAULT 'running', -- running | paused | done | failed | cancelled
    current_step  text NOT NULL DEFAULT '',
    context       jsonb NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS workflow_runs_workflow_idx ON workflow_runs (workflow_id);

-- Append-only event log, same invariant as mission_events: seq is
-- assigned under a SELECT ... FOR UPDATE on the parent run row, never
-- updated or deleted. PRIMARY KEY (run_id, seq) mirrors
-- mission_events/session_events: no surrogate id, seq is already the
-- per-run ordering key.
CREATE TABLE IF NOT EXISTS workflow_run_events (
    run_id      uuid NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    seq         bigint NOT NULL,
    kind        text NOT NULL,
    payload     jsonb NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, seq)
);

-- Missions are long-running, agent-driven units of work distinct from
-- chat sessions: a mission survives across many model turns, tracks a
-- plan and progress log, and drives itself through a fixed phase
-- pipeline (discover -> plan -> generate -> prove -> result ->
-- done|failed) under a state machine (internal/brain/missions).
--
-- phase and status are deliberately NOT CHECK-constrained. A future
-- code rollback that doesn't recognize a newer phase/status value must
-- degrade the row to a safe paused/infra state in Go (parsePhase/
-- parseStatus), not have Postgres reject it outright: corruption-
-- safety over strictness at the schema layer.
CREATE TABLE IF NOT EXISTS missions (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    goal                  text NOT NULL,
    kind                  text NOT NULL CHECK (kind IN ('coding', 'general')),
    agent_id              uuid REFERENCES agents(id),
    phase                 text NOT NULL DEFAULT 'discover',
    status                text NOT NULL DEFAULT 'idle',
    pause_reason          text NOT NULL DEFAULT '',
    pause_message         text NOT NULL DEFAULT '',
    workspace             text NOT NULL DEFAULT '',
    worktree              text NOT NULL DEFAULT '',
    branch                text NOT NULL DEFAULT '',
    base_commit           text NOT NULL DEFAULT '',
    spec                  jsonb NOT NULL DEFAULT '{}',
    progress              jsonb NOT NULL DEFAULT '[]',
    iteration             integer NOT NULL DEFAULT 0,
    max_iterations        integer NOT NULL DEFAULT 8,
    consecutive_failures  integer NOT NULL DEFAULT 0,
    last_gap_fingerprint  text NOT NULL DEFAULT '',
    stall_count           integer NOT NULL DEFAULT 0,
    budget_amount         numeric(12,2),
    budget_currency       char(3) NOT NULL DEFAULT 'USD',
    route                 text NOT NULL DEFAULT '',
    review_route          text NOT NULL DEFAULT '',
    -- PlanRoute, when set, is the route oversight phases (discover, plan,
    -- replan) run on instead of route -- "GLM plans, local generates":
    -- worker/generate turns keep running on route while oversight runs on
    -- a stronger model. '' (the default) means route covers everything,
    -- exact prior behavior. review_route still overrides for prove
    -- specifically: precedence there is review_route > plan_route >
    -- route (internal/brain/missions/runner.go's reviewRoute).
    plan_route            text NOT NULL DEFAULT '',
    -- RouteModel/PlanRouteModel/ReviewRouteModel (D-078) each pin one
    -- phase axis to one exact chain entry in the route it would
    -- otherwise resolve, as "provider name/model" (router.go's
    -- splitProviderModelHint) -- '' means today's first-usable walk.
    -- Precedence mirrors the route helpers exactly: route_model backs
    -- generate (workerRoute), plan_route_model backs discover/plan
    -- (oversightRoute), review_route_model falls back review_route_model
    -- > plan_route_model > route_model (runner.go's reviewRouteModel).
    -- Escalation is never pinned: it is a failure-path fallback and a
    -- stuck pin would defeat its purpose (workerRoute clears route_model
    -- when it swaps to escalation_route). A pin naming an entry the
    -- chain no longer has just fails to match and the normal
    -- first-usable walk runs -- never validated to exist at write time.
    route_model           text NOT NULL DEFAULT '',
    plan_route_model      text NOT NULL DEFAULT '',
    review_route_model    text NOT NULL DEFAULT '',
    -- PendingPermission carries the broker-issued park (issue #423):
    -- NULL means no pending request, otherwise
    -- {"id", "tool", "args", "danger", "rationale"}, the same detail
    -- the UI needs to render a real decision prompt instead of a bare
    -- "waiting" banner, mirrors chat's PermissionRequestEvent. Bundled
    -- into one jsonb column rather than five text columns since the
    -- fields are always read/written/cleared together.
    pending_permission    jsonb,
    schedule_id           uuid REFERENCES schedules(id),
    -- ParentMissionID names the terminal mission this one follows up
    -- on (api/missions.go's create); parents are terminal, exactly the
    -- rows Delete can remove, so SET NULL keeps a follow-up mission
    -- valid rather than blocking its parent's deletion.
    parent_mission_id     uuid REFERENCES missions(id) ON DELETE SET NULL,
    -- ParentContext is an immutable outcome-digest snapshot of the
    -- parent mission taken at follow-up create time (missions.OutcomeDigest),
    -- rendered into the follow-up's discover/plan/work prompts.
    parent_context        text NOT NULL DEFAULT '',
    -- ReferencedContext is an immutable digest of the composer #-mention
    -- references (missions/sessions/kb docs) picked at create time,
    -- resolved via chat.Service's reference resolver -- rendered into
    -- discover/plan/work prompts additive to parent_context, not a
    -- replacement for it (a mission can be both a follow-up AND carry
    -- its own picked references).
    referenced_context    text NOT NULL DEFAULT '',
    -- Attachments is a jsonb array of {id, mime, name, markdown}: id
    -- names an attachments-store row, markdown is the PDF's markitdown
    -- conversion snapshotted ONCE at create time (re-conversion drift
    -- would rewrite earlier prompts, api/missions.go's create).
    attachments           jsonb NOT NULL DEFAULT '[]',
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    -- Opt-in escalation ladder: when set, worker turns switch to this
    -- route after a worker failure or review rework instead of burning
    -- more iterations on a model that already proved too weak for the
    -- unit. Empty (the default) disables escalation entirely -- route
    -- changes must never be a surprise cost jump.
    escalation_route      text NOT NULL DEFAULT '',
    -- PromptOverlay snapshots the creating agent's prompt_overlay at
    -- create time (like route/review_route already do): a mission
    -- outlives the request that made it, so it can't re-resolve a live
    -- agent lookup later without risking a surprise prompt change
    -- mid-mission if the agent row is edited while the mission runs.
    prompt_overlay        text NOT NULL DEFAULT '',
    -- Knowledge snapshots the creating agent's kb_collections allowlist
    -- at create time, same reasoning as prompt_overlay above: a
    -- mission outlives the request that made it, so search_kb's
    -- collection scoping can't re-resolve a live agent lookup later.
    -- Empty array means search_kb is never offered on this mission's
    -- turns, regardless of what the agent row says today.
    knowledge             jsonb NOT NULL DEFAULT '[]',
    -- Harness snapshots the operator's execution-strategy choice for a
    -- coding mission's worker turns at create time, never re-read from
    -- settings at dispatch. "" is native; "claude-cli" (etc) names a
    -- registered delegated executor (internal/brain/missions/executor).
    harness               text NOT NULL DEFAULT '',
    -- Light missions (D-069, general kind only) skip discover/plan/
    -- prove: born in phase=generate, one bare worker turn, the final
    -- worker message is the deliverable, then result/done.
    light                 boolean NOT NULL DEFAULT false,
    -- Mission worker turns run through loop.Agent same as chat, but
    -- tool-call bookkeeping (session_events, tools audit) hard-requires
    -- a real session_id uuid FK -- a mission has no chat session of its
    -- own. Give every mission a hidden session row purely so that
    -- bookkeeping has something real to attach to; nothing about it is
    -- chat-facing (no title shown, sessions list can filter it out by
    -- join).
    session_id            uuid REFERENCES sessions(id),
    -- Missions run for hours unattended; per-command-shape approval
    -- (built for a human watching a chat session) would otherwise park
    -- a mission on every novel-but-harmless shell call. Default true:
    -- new missions auto-approve DangerSafe shell calls via a standing
    -- session grant set at creation (Driver.Create) -- destructive-
    -- classified commands still always ask, unaffected by this column
    -- or any grant.
    auto_approve_safe     boolean NOT NULL DEFAULT true,
    -- D-087 (issue #456): true (default) advances plan -> generate the
    -- moment a plan lands, byte-identical to every mission before this
    -- column existed. false parks the mission on pause_reason='approval'
    -- instead, waiting for an operator approve/replan/rediscover verb --
    -- never auto-resumed by any sweep, an approval decision has no safe
    -- default. Forced true at creation for scheduler-fired and
    -- workflow-spawned missions regardless of template/step input.
    auto_approve_plan     boolean NOT NULL DEFAULT true,
    -- The reviewer judges the baseline git diff, but a general
    -- mission never touches tracked files -- its diff is
    -- always empty, so the reviewer previously had zero evidence to
    -- judge and rejected every round. This carries the worker's own
    -- mission_status evidence text forward from generate to prove,
    -- alongside (not instead of) the diff for coding missions.
    last_evidence         text NOT NULL DEFAULT '',
    -- The discover phase's findings, carried into the plan phase's
    -- prompt (internal/brain/missions/driver.go's runPlan) so the
    -- planner sees what exploration turned up, not just the bare goal.
    explore_notes         text NOT NULL DEFAULT '',
    -- A mission gets exactly one automatic replan attempt on stall
    -- (statemachine.go's stepWorkerRetry/stepReviewRework) before a
    -- second identical stall pauses for a human, same as always.
    replan_used           boolean NOT NULL DEFAULT false,
    -- Environment selects the per-language sandbox image (D-05x,
    -- sandboxd's image allowlist) a coding mission's container runs.
    -- Unlike harness, this has NO settings default: precedence is
    -- explicit request -> auto-detect from repo markers at
    -- provisioning (driver.go's ensureProvisioned) -> base ("").
    -- Sticky once detected (store.SetEnvironment) so a mission never
    -- re-detects mid-run. General missions never set this.
    environment           text NOT NULL DEFAULT '',
    -- FinalOutput is a light mission's verbatim final worker message
    -- (D-069), the deliverable itself, since destinations delivery has
    -- no other body content for a mission with no review/artifacts.
    final_output          text NOT NULL DEFAULT '',
    -- Short display name, generated once (store.SetNameIfEmpty) the
    -- same way a chat session's title is (chat.go's autoTitle): a
    -- one-shot best-effort gateway call after creation, never blocking
    -- or failing creation. Scheduler-fired missions get the schedule's
    -- own name directly, no LLM call. Empty means generation hasn't
    -- landed yet (or a scheduler mission predates this column); the UI
    -- falls back to a truncated goal. Never re-summarized once set.
    name                  text NOT NULL DEFAULT '',
    -- A coding mission can clone an existing GitHub repo instead of
    -- self-initializing an empty one (Workspace.Provision): repo_url is
    -- the repo's https clone URL, connector_id names the github-kind
    -- connectors row whose PAT authenticates the clone. Both empty
    -- (the default) is the existing self-init behavior; repo_url
    -- without a connector_id is rejected at create time (api/missions.go)
    -- -- v1 has no anonymous-clone path. The clone auth token itself is
    -- never persisted here or anywhere else: it's resolved fresh from
    -- connector_id's credential_ref at provisioning time only.
    repo_url              text NOT NULL DEFAULT '',
    connector_id          text NOT NULL DEFAULT '',
    -- Consent-at-create for the mission's auto-completion action: ''
    -- (default) does nothing in the result phase's step; 'push'
    -- pushes the branch; 'push_pr' pushes then opens a pull request.
    -- Chosen by the operator at create time (api/missions.go), never
    -- decided by the model -- keeps the pushes-stay-human invariant:
    -- the harness only ever executes a choice a human already made.
    -- Requires repo_url+connector_id and kind='coding', same guards as
    -- the manual push/pr endpoints.
    on_complete           text NOT NULL DEFAULT '',
    -- This mission's own override of the settings-configured git
    -- strategy defaults (settings.ValueGitBranchPattern/
    -- ValueGitCommitStyle): '' (the default) means "use the settings
    -- default," resolved fresh at provisioning/commit time
    -- (internal/brain/missions/driver.go), never baked in here.
    -- branch_pattern is a validated template (internal/brain/missions/
    -- branchtemplate.go); commit_style is 'conventional' or 'plain'.
    branch_pattern        text NOT NULL DEFAULT '',
    commit_style          text NOT NULL DEFAULT '',
    -- Destination ids to deliver this mission's outcome digest to in
    -- the result phase's step (destinations.go's Deliverer,
    -- driver.go's runResult, D-086). Never model-decided: api/missions.go's
    -- create validates every id against the operator-owned destinations
    -- table before it lands here.
    destination_ids       uuid[] NOT NULL DEFAULT '{}',
    -- D-081 (issue #370): kb collection to promote this mission's
    -- markdown artifacts into in the result phase's step
    -- (kb.go's promoteToKB, driver.go's runResult, D-086). ''
    -- (default) does nothing. Explicit id, never a default "Missions"
    -- collection auto-create, same as destination_ids above; the
    -- operator can also promote manually via POST .../promote-kb after
    -- the mission is done, using the same code path.
    promote_kb_collection_id uuid,
    -- workflow_run_id/workflow_step name the workflow run and step
    -- (internal/brain/workflows) this mission was spawned as, if any.
    -- NULL/'' for an ordinary mission. The workflow engine reads these
    -- via mission terminal events; it never writes mission state.
    workflow_run_id        uuid REFERENCES workflow_runs(id),
    workflow_step          text NOT NULL DEFAULT '',
    -- ArtifactRefs: this mission's declared artifact files, best-effort
    -- copied into the attachment store in the result phase's step
    -- (driver.go's copyArtifacts, D-086): a jsonb array of {id, mime, name},
    -- mirroring attachments' own shape. Never bytes (D-045). Lets a
    -- mission's result artifacts survive workspace deletion, unlike the
    -- live-workspace files ArtifactsSection browses.
    artifact_refs          jsonb NOT NULL DEFAULT '[]',
    -- Per-mission override for how long an unanswered pending_permission
    -- may stay parked before the periodic sweep (missions/sweep.go)
    -- auto-denies it (issue #445). NULL inherits the global
    -- permission_timeout_seconds setting; the setting itself defaults to
    -- 0 (disabled, park forever) so this is opt-in and changes nothing
    -- for an existing deployment that never sets it.
    permission_timeout_seconds integer,
    -- PendingInput is ask_user's park (D-088, issue #457), a second park
    -- kind alongside pending_permission: NULL means no pending question,
    -- otherwise {question, kind, options, proposed_default, asked_at,
    -- phase}. Mirrors pending_permission's shape/lifecycle exactly
    -- (store.go's SetPendingInput/ClearPendingInput).
    pending_input         jsonb,
    -- AsksUsed counts ask_user calls this mission has spent, enforced
    -- against askBudget (missions/asktool.go); a third call over
    -- budget is a plain tool error back to the model, no park.
    asks_used              integer NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS missions_status_idx ON missions (status);
-- The work-slot cap (ClaimWorkSlot) and the boot sweep both need
-- "which missions are actively occupying a slot," cheaply.
CREATE INDEX IF NOT EXISTS missions_active_idx ON missions (phase) WHERE phase NOT IN ('done', 'failed');

-- Append-only event log, the mission's audit trail and the Timeline
-- UI's data source. seq is assigned under a SELECT ... FOR UPDATE on
-- the parent mission row (serializes appends per-mission only, not
-- globally), see internal/brain/missions/store.go AppendEvent.
CREATE TABLE IF NOT EXISTS mission_events (
    mission_id  uuid NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    seq         bigint NOT NULL,
    kind        text NOT NULL,
    payload     jsonb NOT NULL DEFAULT '{}',
    -- provenance distinguishes real driver output from test/replay
    -- fixtures written into the same table for scenario tests.
    provenance  text NOT NULL DEFAULT 'live' CHECK (provenance IN ('live', 'test', 'replay')),
    fingerprint text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mission_id, seq)
);

-- Durable notification inbox (internal/brain/missions/notify.go):
-- always written for actionable transitions regardless of whether the
-- best-effort webhook fan-out succeeds.
CREATE TABLE IF NOT EXISTS notifications (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mission_id  uuid NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    kind        text NOT NULL,
    message     text NOT NULL DEFAULT '',
    read        boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS notifications_unread_idx ON notifications (mission_id) WHERE NOT read;
-- Plain (non-partial) index for the FK cascade's own lookup (DELETE
-- FROM missions cascading into notifications), distinct from the
-- partial unread index above which only covers List's unread-first path.
CREATE INDEX IF NOT EXISTS notifications_mission_idx ON notifications (mission_id);

-- Content-addressed image attachments (internal/brain/attachments):
-- binaries live on the ATTACHMENTS_DIR volume as <sha256><ext>, never
-- in Postgres; this table is metadata only, keyed by that same sha256
-- so re-uploading identical bytes is a no-op (D-045).
CREATE TABLE IF NOT EXISTS attachments (
    id         text PRIMARY KEY,
    mime       text NOT NULL,
    size_bytes bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Knowledge base: named collections of ingested documents an agent can
-- search with search_kb (D-060). Unlike memories, KB content is
-- MUTABLE reference data: external documentation the operator curates,
-- not something Timothy observed. Re-ingesting a document deletes its
-- existing chunks and rewrites them; there is no supersede chain here.
--
-- D-060: KB is external memory, distinct from extracted long-term
-- memory (D-011). When the two disagree, facts sourced from KB outrank
-- extracted memories (a curated doc beats an inferred fact), but
-- user-stated preferences from memory outrank KB (the user's own words
-- beat any document). KB chunks are never written into the memories
-- table, the two stores stay separate, resolved at use time, not
-- merged at write time.

CREATE TABLE IF NOT EXISTS kb_collections (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text UNIQUE NOT NULL,
    description text NOT NULL DEFAULT '',
    -- retrieval_weight (D-085, issue #443) down-weights or up-weights a
    -- whole collection at retrieval time: a low weight keeps
    -- identity/profile collections out of general topical contests
    -- while still retrievable when they are the only relevant content.
    retrieval_weight double precision NOT NULL DEFAULT 1.0 CHECK (retrieval_weight > 0 AND retrieval_weight <= 2),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS kb_documents (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    collection_id uuid NOT NULL REFERENCES kb_collections (id) ON DELETE CASCADE,
    title         text NOT NULL,
    source_type   text NOT NULL DEFAULT 'file',
    source_ref    text NOT NULL DEFAULT '',
    -- D-080: provenance ranks operator-vetted content above
    -- model-written content at retrieval time (issue #372), distinct
    -- from source_type (which names the ingestion mechanism, not who
    -- authored the content). source_ref already carries a reference
    -- (URL, mission:<id> once #370 promotes mission artifacts) usable
    -- alongside it.
    provenance    text NOT NULL DEFAULT 'curated'
        CHECK (provenance IN ('curated', 'mission', 'web')),
    -- markdown is the markitdown conversion of the uploaded file,
    -- persisted so a re-ingest never re-calls the sidecar (mirrors
    -- mission attachments, D-05x); never served over the admin API.
    markdown      text NOT NULL DEFAULT '',
    status        text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'ingesting', 'ready', 'failed')),
    error         text NOT NULL DEFAULT '',
    bytes         bigint NOT NULL DEFAULT 0,
    chunk_count   int NOT NULL DEFAULT 0,
    -- retry_count/next_retry_at back the auto-retry sweep (issue #414):
    -- a failed document classified as a transient error gets re-ingested
    -- with bounded attempts and backoff; a permanent error leaves both
    -- untouched and the document stays only manually reingestable.
    retry_count   int NOT NULL DEFAULT 0,
    next_retry_at timestamptz,
    ingested_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS kb_documents_collection_idx ON kb_documents (collection_id);

CREATE TABLE IF NOT EXISTS kb_chunks (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id     uuid NOT NULL REFERENCES kb_documents (id) ON DELETE CASCADE,
    seq             int NOT NULL,
    breadcrumb      text NOT NULL DEFAULT '',
    content         text NOT NULL,
    -- Same dimension as memories.embedding (above), both ride the
    -- gateway's one configured embedding model.
    embedding       vector(1024),
    embedding_model text NOT NULL DEFAULT '',
    tsv             tsvector GENERATED ALWAYS AS (to_tsvector('english', content)) STORED,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (document_id, seq)
);

CREATE INDEX IF NOT EXISTS kb_chunks_embedding_hnsw
    ON kb_chunks USING hnsw (embedding vector_cosine_ops)
    WITH (m = 20, ef_construction = 200);

CREATE INDEX IF NOT EXISTS kb_chunks_tsv_gin ON kb_chunks USING gin (tsv);

CREATE INDEX IF NOT EXISTS kb_chunks_document_idx ON kb_chunks (document_id);

-- Destinations: operator-created outbound sinks missions deliver
-- results to (internal/brain/destinations). config is per-kind and
-- never a secret value; credential_ref names a secret store ref, used
-- by telegram destinations (unused for email/webhook).
CREATE TABLE destinations (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL UNIQUE,
    kind        text NOT NULL CHECK (kind IN ('email', 'webhook', 'telegram')),
    config      jsonb NOT NULL DEFAULT '{}',
    credential_ref text NOT NULL DEFAULT '',
    enabled     boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- Content-hash cache for the pdfgen sidecar (internal/brain/pdfgen):
-- input_hash maps a generation request (generator version + options +
-- documents) to the attachment id it already produced, so a repeat
-- request returns the cached PDF without recompiling.
CREATE TABLE IF NOT EXISTS pdf_renders (
    input_hash text PRIMARY KEY,
    attachment_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
