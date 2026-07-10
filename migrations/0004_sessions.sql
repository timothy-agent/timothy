-- Event-sourced sessions: an append-only log per session with two
-- projections (UI transcript, LLM context) derived in code. The
-- minimal sessions table gains the columns real session management
-- needs.

ALTER TABLE sessions ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS archived boolean NOT NULL DEFAULT false;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS last_category text NOT NULL DEFAULT '';

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
