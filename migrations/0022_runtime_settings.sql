-- Typed runtime settings alongside the boolean feature switches:
-- string values editable from the Settings UI, replacing env knobs
-- (SESSION_TOKEN_BUDGET, SKILLS_ALLOWLIST). Empty/absent value means
-- the built-in default.
CREATE TABLE IF NOT EXISTS runtime_settings (
    key        text PRIMARY KEY,
    value      text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);
