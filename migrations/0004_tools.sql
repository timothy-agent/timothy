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
    bytes      int NOT NULL,
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
