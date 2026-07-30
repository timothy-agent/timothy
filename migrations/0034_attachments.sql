-- Content-addressed image attachments (internal/brain/attachments):
-- binaries live on the ATTACHMENTS_DIR volume as <sha256><ext>, never
-- in Postgres; this table is metadata only, keyed by that same sha256
-- so re-uploading identical bytes is a no-op (D-045).
CREATE TABLE IF NOT EXISTS attachments (
    id         text PRIMARY KEY,
    mime       text NOT NULL,
    size_bytes bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
