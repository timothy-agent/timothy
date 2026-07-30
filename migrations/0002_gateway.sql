CREATE TABLE IF NOT EXISTS providers (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name           text UNIQUE NOT NULL,
    kind           text NOT NULL CHECK (kind IN ('api', 'cli')),
    driver         text NOT NULL,
    base_url       text NOT NULL DEFAULT '',
    default_model  text NOT NULL DEFAULT '',
    models         jsonb NOT NULL DEFAULT '[]',
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
CREATE TABLE IF NOT EXISTS routes (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text UNIQUE NOT NULL,
    chain      jsonb NOT NULL DEFAULT '[]',
    strategy   text NOT NULL DEFAULT 'ordered'
        CHECK (strategy IN ('ordered', 'auto', 'price', 'latency')),
    enabled    boolean NOT NULL DEFAULT false,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS cost_ledger (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    ts                 timestamptz NOT NULL DEFAULT now(),
    provider           text NOT NULL,
    model              text NOT NULL,
    route              text NOT NULL,
    agent              text NOT NULL DEFAULT '',
    session_id         text,
    lane_id            text,
    input_tokens       integer,
    output_tokens      integer,
    cache_read_tokens  integer,
    cache_write_tokens integer,
    latency_ms         integer NOT NULL,
    status             text NOT NULL,
    error_code         text,
    cost_usd           numeric(12, 6)
);

CREATE INDEX IF NOT EXISTS cost_ledger_ts_idx ON cost_ledger (ts);
CREATE INDEX IF NOT EXISTS cost_ledger_session_idx ON cost_ledger (session_id) WHERE session_id IS NOT NULL;

-- Seed only the route NAMES the code references — no providers, no
-- chains, no opinions (the user configures providers in Settings and
-- fills these chains there). Fixed names and their consumers:
--   'default'   — chat's fallback route (internal/brain/chat)
--   'summarize' — session compactor (internal/brain/session)
--   'embedding' — gateway /v1/embeddings (internal/gateway/api)
--   'research'  — min-retrieval floor keys on this route name (D-009)
--                 and the researcher agent routes here
--   'vision'    — image messages auto-flip here (D-046,
--                 internal/brain/chat); gateway falls back to
--                 'default' when this route is missing/disabled/empty
-- All start with an empty chain and disabled.
INSERT INTO routes (name)
VALUES ('default'), ('summarize'), ('embedding'), ('research'), ('vision')
ON CONFLICT (name) DO NOTHING;
