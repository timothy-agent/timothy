-- Dashboard aggregation reads the ledger by time range then groups by
-- provider/model/category; the composite index covers the scan the
-- existing ts-only index cannot.
CREATE INDEX IF NOT EXISTS cost_ledger_ts_dims_idx
    ON cost_ledger (ts, provider, model, task_category);
