-- Brain-owned runtime settings (D-032): boolean feature switches and
-- typed string knobs in one table, value as jsonb. Absent key = the
-- built-in default (switches enabled, knobs empty): defaults live in
-- code so a fresh database behaves like before this table existed.
CREATE TABLE IF NOT EXISTS settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Spend budgets for the alert surface: at most one limit per window,
-- scoped to a single currency (no FX conversion anywhere in this
-- codebase — spend is only ever compared within the same currency).
-- Absent row = no budget set, nothing to alert on.
CREATE TABLE IF NOT EXISTS spend_budgets (
    scope         text PRIMARY KEY CHECK (scope IN ('day', 'month')),
    limit_amount  numeric(12, 2) NOT NULL CHECK (limit_amount > 0),
    currency      char(3) NOT NULL DEFAULT 'USD',
    updated_at    timestamptz NOT NULL DEFAULT now()
);
