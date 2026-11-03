-- HNSW cosine index on chunk_embeddings.embedding (Technical PRD §5).
-- Same name and params as scripts/benchmark_hnsw.py so databases where the
-- benchmark already built it stay a no-op. Params are fixed by ADR-0008.
CREATE INDEX IF NOT EXISTS chunk_embeddings_hnsw_idx
  ON chunk_embeddings USING hnsw (embedding halfvec_cosine_ops)
  WITH (m = 16, ef_construction = 64);
