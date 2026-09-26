-- Dense vectors from the ai-worker (all-MiniLM-L6-v2, 384 dims). Kept in the
-- system of record so a reindex can rebuild the kNN index without re-embedding.
ALTER TABLE repositories ADD COLUMN IF NOT EXISTS embedding REAL[];
