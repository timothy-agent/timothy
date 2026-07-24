-- The reviewer judges the baseline git diff, but a research/scheduled
-- mission never touches tracked files -- its diff is always empty, so
-- the reviewer previously had zero evidence to judge and rejected
-- every round. This carries the worker's own mission_status evidence
-- text forward from execute to review, alongside (not instead of) the
-- diff for coding missions.
ALTER TABLE missions ADD COLUMN IF NOT EXISTS last_evidence text NOT NULL DEFAULT '';
