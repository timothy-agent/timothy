-- Brain-owned runtime settings (D-032): boolean feature switches and
-- typed string knobs in one table, value as jsonb. Absent key = the
-- built-in default (switches enabled, knobs empty): defaults live in
-- code so a fresh database behaves like before this table existed.
CREATE TABLE IF NOT EXISTS settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Spend budgets for the alert surface: at most one USD limit per
-- window. Absent row = no budget set, nothing to alert on.
CREATE TABLE IF NOT EXISTS spend_budgets (
    scope      text PRIMARY KEY CHECK (scope IN ('day', 'month')),
    limit_usd  numeric(12, 2) NOT NULL CHECK (limit_usd > 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);
