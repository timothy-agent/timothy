-- Spend budgets for the alert surface: at most one USD limit per
-- window. Absent row = no budget set, nothing to alert on.
CREATE TABLE IF NOT EXISTS spend_budgets (
    scope      text PRIMARY KEY CHECK (scope IN ('day', 'month')),
    limit_usd  numeric(12, 2) NOT NULL CHECK (limit_usd > 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);
