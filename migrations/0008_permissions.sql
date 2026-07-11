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
