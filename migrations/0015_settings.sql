-- Brain-owned feature switches. Absent key = enabled: defaults live in
-- code so a fresh database behaves like before this table existed.
CREATE TABLE IF NOT EXISTS settings (
    key        text PRIMARY KEY,
    value      boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
