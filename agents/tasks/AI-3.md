# AI-3: Batch embedding calls in RAG citation validation

| | |
|---|---|
| **Owner** | ai-engineer |
| **Priority** | P1 |
| **PRD ref** | Technical PRD §11 "Batch embedding in RAG validation"; Product PRD §8 `/v1/ask` p95 < 3 s |
| **Depends on** | AI-1 (tests lock behaviour), AI-2 (same code block; avoids merge conflicts) |
| **Status** | Ready (start after AI-1 and AI-2 merge) |

## Goal
`Ask` in `internal/query/rag.go` calls `EmbedTexts` once per hit (chunk) and once per cited sentence, which makes K + S
sequential HTTP round-trips to the embedder API. `EmbedTexts` already accepts a slice. Making one call for all chunks
and one call for all distinct cited sentences cuts `/v1/ask` latency, and QA-8 will produce more sentences.

## Scope
- `internal/query/rag.go`: at most 2 `EmbedTexts` calls per `Ask` (chunks, then deduplicated cleaned sentences). Keep the results in input order. Keep AI-2's fail-closed behaviour: if a batch call fails, every citation that depends on it is unsupported with score 0.
- `internal/query/rag_test.go`: a fake embedder that counts calls. Assert ≤ 2 calls for an answer with 3 hits and 2 cited sentences. Assert that labels and scores equal the per-item results from the existing tests.
- Out of scope: changing thresholds, the response shape, or the embedder API's batch size (32 on the Python side).

## Acceptance criteria
- [ ] AC1 — The call-count test passes (≤ 2 `EmbedTexts` calls per `Ask`).
- [ ] AC2 — All existing AI-1/AI-2 tests pass unchanged (no test edited to fit the new code).
- [ ] AC3 — `make eval`: supported-citation rate unchanged (±0.01) and `/v1/ask` p50/p95 before/after pasted, or "not run: infra unavailable" plus the exact command.
- [ ] AC4 — Technical PRD §11 row status updated in the PR.

## Verification
```bash
go test -cover ./internal/query/...
make test-unit
make lint
make eval   # when infra is available
```

## Notes
- Specced by pm-lead in PM-1 from the board Backlog item "Batch embedding in RAG validation".
