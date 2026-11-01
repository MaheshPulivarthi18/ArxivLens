# OPS-H5: Create the HNSW vector index in a migration, and apply all migrations in order

| | |
|---|---|
| **Owner** | backend-engineer |
| **Priority** | P1 |
| **PRD ref** | QA-1; Product PRD §8 `/v1/search` p95 < 1.5 s; Technical PRD §5 (HNSW gap note) |
| **Depends on** | — (soft: OPS-H1 for AC5; without it, start the workers by hand per AGENTS.md §5) |
| **Status** | Ready |

## Goal
Technical PRD §5 promises an HNSW cosine index on `chunk_embeddings.embedding`, but no migration creates it. Only
`scripts/benchmark_hnsw.py` does (`chunk_embeddings_hnsw_idx`, `m=16, ef_construction=64`). On a fresh `make migrate`,
vector search is therefore an exact scan, and `tests/test_m5_m6_telemetry_eval.py::test_pgvector_hnsw_index_presence_and_querying`
passes only if the benchmark script happened to run first. Also, `arxivlens migrate` executes a single file
(`RunMigrations(ctx, migrationFile)`), so a second migration would never be applied.

## Scope
- Files: new `migrations/000002_chunk_embeddings_hnsw.up.sql` and `.down.sql`, `internal/db/db.go` and/or `cmd/arxivlens/main.go` (`migrate` applies every `*.up.sql` in lexical order), and a Go unit test for the file ordering (no DB).
- The index must use exactly the existing name and params: `CREATE INDEX IF NOT EXISTS chunk_embeddings_hnsw_idx ON chunk_embeddings USING hnsw (embedding halfvec_cosine_ops) WITH (m = 16, ef_construction = 64);`, so that existing databases where the benchmark already created it stay a no-op. Changing the params is out of scope (ADR-0008).
- Migrations must stay idempotent (`IF NOT EXISTS`), because there is no schema-version table. Adding a version table is out of scope.
- Never edit `000001_init_schema.*.sql`.
- Out of scope: `scripts/benchmark_hnsw.py` (ai-engineer may later drop its CREATE INDEX), and query SQL changes.

## Acceptance criteria
- [ ] AC1 — On a fresh database, `make migrate` creates `chunk_embeddings_hnsw_idx`. Paste the `\d chunk_embeddings` or `pg_indexes` output.
- [ ] AC2 — Running `make migrate` twice in a row succeeds (idempotent).
- [ ] AC3 — Go unit test: the migration runner selects `*.up.sql` only, in lexical order (`000001` before `000002`). Passes with no DB.
- [ ] AC4 — `EXPLAIN` of the vector CTE in the hybrid query from `internal/query` is pasted in the PR, with the planner's choice noted. If the planner does not use the index (e.g. because of the join or model filter), open a follow-up request in Notes instead of changing the query here.
- [ ] AC5 — `make test-full` M1 and M5/M6 pass on a fresh stack without running `scripts/benchmark_hnsw.py` first. Use OPS-H1's `make stack-up` if merged; otherwise start the processor/embedder workers by hand (AGENTS.md §5) and say so in the PR. If no Docker is available, write "not run: infra unavailable" plus the exact commands. AC1–AC3 still need evidence.

## Verification
```bash
make test-unit
make lint
make infra-up && make migrate && make migrate
psql "$DATABASE_URL" -c "SELECT indexname, indexdef FROM pg_indexes WHERE tablename='chunk_embeddings';"
make test-full
```

## Notes
- Specced by pm-lead in PM-1 after finding that no migration creates the HNSW index (`grep -rni hnsw migrations/` is empty).
- Adding an index through a new migration needs no ADR (AGENTS.md §1).
