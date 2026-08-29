-- Knowledge base: named collections of ingested documents an agent can
-- search with search_kb (D-060). Unlike memories, KB content is
-- MUTABLE reference data — external documentation the operator curates,
-- not something Timothy observed. Re-ingesting a document deletes its
-- existing chunks and rewrites them; there is no supersede chain here.
--
-- D-060: KB is external memory, distinct from extracted long-term
-- memory (D-011). When the two disagree, facts sourced from KB outrank
-- extracted memories (a curated doc beats an inferred fact), but
-- user-stated preferences from memory outrank KB (the user's own words
-- beat any document). KB chunks are never written into the memories
-- table — the two stores stay separate, resolved at use time, not
-- merged at write time.

CREATE TABLE IF NOT EXISTS kb_collections (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text UNIQUE NOT NULL,
    description text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS kb_documents (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    collection_id uuid NOT NULL REFERENCES kb_collections (id) ON DELETE CASCADE,
    title         text NOT NULL,
    source_type   text NOT NULL DEFAULT 'file',
    source_ref    text NOT NULL DEFAULT '',
    content_hash  text NOT NULL DEFAULT '',
    -- markdown is the markitdown conversion of the uploaded file,
    -- persisted so a re-ingest never re-calls the sidecar (mirrors
    -- mission attachments, D-05x); never served over the admin API.
    markdown      text NOT NULL DEFAULT '',
    status        text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'ingesting', 'ready', 'failed')),
    error         text NOT NULL DEFAULT '',
    bytes         bigint NOT NULL DEFAULT 0,
    chunk_count   int NOT NULL DEFAULT 0,
    ingested_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS kb_documents_collection_idx ON kb_documents (collection_id);

CREATE TABLE IF NOT EXISTS kb_chunks (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id     uuid NOT NULL REFERENCES kb_documents (id) ON DELETE CASCADE,
    seq             int NOT NULL,
    breadcrumb      text NOT NULL DEFAULT '',
    content         text NOT NULL,
    -- Same dimension as memories.embedding (0005_memory.sql) — both
    -- ride the gateway's one configured embedding model.
    embedding       vector(1024),
    embedding_model text NOT NULL DEFAULT '',
    tsv             tsvector GENERATED ALWAYS AS (to_tsvector('english', content)) STORED,
    metadata        jsonb NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (document_id, seq)
);

CREATE INDEX IF NOT EXISTS kb_chunks_embedding_hnsw
    ON kb_chunks USING hnsw (embedding vector_cosine_ops)
    WITH (m = 20, ef_construction = 200);

CREATE INDEX IF NOT EXISTS kb_chunks_tsv_gin ON kb_chunks USING gin (tsv);

CREATE INDEX IF NOT EXISTS kb_chunks_document_idx ON kb_chunks (document_id);
