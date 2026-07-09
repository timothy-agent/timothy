-- Minimal sessions table: real event-sourced sessions replace these
-- semantics next; this only anchors session ids for ledger attribution.
CREATE TABLE IF NOT EXISTS sessions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title      text,
    created_at timestamptz NOT NULL DEFAULT now()
);
