-- HNSW cosine index on chunk_embeddings.embedding (Technical PRD §5).
-- Same name and params as scripts/benchmark_hnsw.py so databases where the
-- benchmark already built it stay a no-op. Params match the benchmark; ADR-0008
-- (Proposed) defers the final choice.
CREATE INDEX IF NOT EXISTS chunk_embeddings_hnsw_idx
  ON chunk_embeddings USING hnsw (embedding halfvec_cosine_ops)
  WITH (m = 16, ef_construction = 64);
