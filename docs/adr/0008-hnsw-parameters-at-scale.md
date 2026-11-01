# ADR-0008: HNSW parameters (`m`, `ef_construction`, `ef_search`) beyond ~1M chunks

- **Status:** Proposed
- **Date:** 2026-11-01
- **Deciders:** Mahesh Pulivarthi (only the human owner sets Accepted/Rejected)
- **Blocks:** nothing now. Any future migration that rebuilds the `chunk_embeddings` HNSW index waits on it.

## Context
Technical PRD open question 3. The HNSW cosine index on `chunk_embeddings.embedding` (`halfvec(768)`) uses
`m = 16, ef_construction = 64` (pgvector defaults). Today it is created only by `scripts/benchmark_hnsw.py`, not by a
migration; OPS-H5 adds it as a new migration with these same defaults (adding an index needs no ADR). `scripts/benchmark_hnsw.py`
sweeps only `ef_search ∈ {20, 40, 80, 160}`. The current corpus is far below 1M chunks, so there are no
measurements at that scale. The latency target is `/v1/search` p95 < 1.5 s on the Technical PRD §9 reference machine.
Changing these parameters later means dropping and recreating that index in a new migration. Applied migrations are never edited.

## Options
1. **Keep defaults (`m=16, ef_construction=64`) and tune only `ef_search` at query time.** No rebuild. Recall may fall at 1M+.
2. **Raise to `m=24–32, ef_construction=128–200`.** Better recall at scale, but slower builds, more memory (`maintenance_work_mem` 256 MB is likely too small), and a bigger index.
3. **Decide with data.** Extend `scripts/benchmark_hnsw.py` to sweep `m` × `ef_construction` on a synthetic or sampled 1M-vector set and record recall vs. exact search, plus build time and p95. Then pick the cheapest setting that keeps vector recall@50 ≥ 0.95 within the latency budget.

## Recommendation (from pm-lead)
Option 3, but only when the corpus approaches ~500k chunks. Until then, Option 1 holds. The ≥ 0.95 recall@50 bar is a pm-lead proposal and needs the
owner's approval. No task is specced now, because it is not urgent at the current corpus size.

## Decision
_(Filled by the human.)_

## Consequences
A rebuild means a new numbered migration and a maintenance window, or a concurrent index build. Compose Postgres tuning in
Technical PRD §9 may need to grow.
