-- Durable notification inbox (internal/brain/missions/notify.go):
-- always written for actionable transitions regardless of whether the
-- best-effort webhook fan-out succeeds.
CREATE TABLE IF NOT EXISTS notifications (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    mission_id  uuid NOT NULL REFERENCES missions(id) ON DELETE CASCADE,
    kind        text NOT NULL,
    message     text NOT NULL DEFAULT '',
    read        boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS notifications_unread_idx ON notifications (mission_id) WHERE NOT read;
