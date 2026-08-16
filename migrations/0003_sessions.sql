-- Event-sourced sessions: an append-only log per session with two
-- projections (UI transcript, LLM context) derived in code.
CREATE TABLE IF NOT EXISTS sessions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title      text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    archived   boolean NOT NULL DEFAULT false,
    last_route text NOT NULL DEFAULT '',
    -- Sessions record which agent serves them; message events carry
    -- the per-turn agent so mid-session switches attribute correctly.
    agent      text NOT NULL DEFAULT '',
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
