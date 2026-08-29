-- Content-hash cache for the pdfgen sidecar (internal/brain/pdfgen):
-- input_hash maps a generation request (generator version + options +
-- documents) to the attachment id it already produced, so a repeat
-- request returns the cached PDF without recompiling.
CREATE TABLE IF NOT EXISTS pdf_renders (
    input_hash text PRIMARY KEY,
    attachment_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
