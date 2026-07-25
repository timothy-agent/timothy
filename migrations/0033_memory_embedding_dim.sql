-- memories.embedding was sized for 1536-dim vectors, but the embedding
-- route serves amazon.titan-embed-text-v2:0 whose widest output is
-- 1024 -- every insert with a vector failed and extraction degraded to
-- embedding-less rows. All stored embeddings are NULL (nothing ever
-- fit), so the narrowing cast is trivial; the hnsw index is rebuilt by
-- the type change.
DO $$
BEGIN
    IF (SELECT atttypmod FROM pg_attribute
        WHERE attrelid = 'memories'::regclass AND attname = 'embedding') <> 1024 THEN
        ALTER TABLE memories ALTER COLUMN embedding TYPE vector(1024);
    END IF;
END $$;
