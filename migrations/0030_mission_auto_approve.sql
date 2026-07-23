-- Missions run for hours unattended; per-command-shape approval (built
-- for a human watching a chat session) would otherwise park a mission
-- on every novel-but-harmless shell call. Default true: new missions
-- auto-approve DangerSafe shell calls via a standing session grant set
-- at creation (Driver.Create) -- destructive-classified commands still
-- always ask, unaffected by this column or any grant.
ALTER TABLE missions ADD COLUMN IF NOT EXISTS auto_approve_safe boolean NOT NULL DEFAULT true;
