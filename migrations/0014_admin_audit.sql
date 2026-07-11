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
