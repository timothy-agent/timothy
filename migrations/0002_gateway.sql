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

-- Routes are named provider/model fallback chains (D-033) — the
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
-- the user can rename at will — code resolves "the route with
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
    -- decision — tracked so subscription runs aren't budget-invisible).
    unbilled           boolean NOT NULL DEFAULT false
);

CREATE INDEX IF NOT EXISTS cost_ledger_ts_idx ON cost_ledger (ts);
CREATE INDEX IF NOT EXISTS cost_ledger_session_idx ON cost_ledger (session_id) WHERE session_id IS NOT NULL;
-- Dashboard aggregation reads the ledger by time range then groups by
-- provider/model/route; this composite index covers that scan.
CREATE INDEX IF NOT EXISTS cost_ledger_ts_dims_idx
    ON cost_ledger (ts, provider, model, route);

-- Every control-plane mutation leaves a row: what changed, from what,
-- to what. Single-token system, so no actor column yet — the bearer IS
-- the admin.
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

-- Seed only the route NAMES the code references — no providers, no
-- chains, no opinions (the user configures providers in Settings and
-- fills these chains there). Fixed roles and their consumers:
--   role 'default'   — chat's fallback route (internal/brain/chat)
--   role 'summarize' — session compactor (internal/brain/session)
--   role 'embedding' — gateway /v1/embeddings (internal/gateway/api)
--   role 'vision'    — image messages auto-flip here (D-046,
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
