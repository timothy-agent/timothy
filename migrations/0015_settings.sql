-- Brain-owned runtime settings (D-032): boolean feature switches and
-- typed string knobs in one table, value as jsonb. Absent key = the
-- built-in default (switches enabled, knobs empty): defaults live in
-- code so a fresh database behaves like before this table existed.
CREATE TABLE IF NOT EXISTS settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
