-- Long-term memory: typed, staged, supersede-only (D-011). Facts are
-- never UPDATEd in place — corrections insert a new row and point the
-- old row's superseded_by at it. Retrieval only ever sees
-- status='active'. Entities and relations stay plain relational
-- tables; no graph database.

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS memories (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type              text NOT NULL CHECK (type IN ('episodic', 'semantic', 'procedural')),
    content           text NOT NULL,
    embedding         vector(1024),
    entity_refs       uuid[] NOT NULL DEFAULT '{}',
    source_session    uuid,
    source_seq        bigint,
    actor             text NOT NULL DEFAULT 'agent',
    created_at        timestamptz NOT NULL DEFAULT now(),
    last_confirmed_at timestamptz NOT NULL DEFAULT now(),
    superseded_by     uuid REFERENCES memories (id),
    status            text NOT NULL CHECK (status IN ('pending', 'active', 'rejected', 'archived')),
    confidence        real,
    tsv               tsvector GENERATED ALWAYS AS (to_tsvector('english', content)) STORED,
    -- Retrieval bookkeeping for the consolidation job (archive episodic
    -- memories unretrieved for 180d) — this is NOT last_confirmed_at,
    -- which only explicit confirmation or re-extraction may bump
    -- (D-011).
    last_retrieved_at timestamptz,
    -- Usage-driven decay bookkeeping (memory-extraction-v2 slice 5):
    -- how many times retrieval has returned this memory. Metadata
    -- bookkeeping, not a fact UPDATE (D-011); memory content stays
    -- supersede-only.
    retrieval_hits    integer NOT NULL DEFAULT 0
);

-- m/ef_construction over pgvector defaults (16/64): better recall at
-- this corpus scale, negligible build cost.
CREATE INDEX IF NOT EXISTS memories_embedding_hnsw
    ON memories USING hnsw (embedding vector_cosine_ops)
    WITH (m = 20, ef_construction = 200);

CREATE INDEX IF NOT EXISTS memories_tsv_gin ON memories USING gin (tsv);

CREATE INDEX IF NOT EXISTS memories_status_type_idx ON memories (status, type);

CREATE TABLE IF NOT EXISTS entities (
    id   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type text NOT NULL CHECK (type IN
        ('person', 'project', 'service', 'preference', 'decision', 'topic', 'place')),
    name text NOT NULL,
    UNIQUE (type, name)
);

CREATE TABLE IF NOT EXISTS relations (
    src        uuid NOT NULL REFERENCES entities (id),
    type       text NOT NULL,
    dst        uuid NOT NULL REFERENCES entities (id),
    valid_from timestamptz NOT NULL DEFAULT now(),
    valid_to   timestamptz
);
