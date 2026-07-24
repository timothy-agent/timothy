-- Opt-in escalation ladder: when set, worker turns switch to this
-- route after a worker failure or review rework instead of burning
-- more iterations on a model that already proved too weak for the
-- unit. Empty (the default) disables escalation entirely -- route
-- changes must never be a surprise cost jump.
ALTER TABLE missions ADD COLUMN IF NOT EXISTS escalation_route text NOT NULL DEFAULT '';
