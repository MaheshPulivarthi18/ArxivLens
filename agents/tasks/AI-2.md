# AI-2: Fail closed when citation support cannot be verified

| | |
|---|---|
| **Owner** | ai-engineer |
| **Priority** | P1 (it inflates the gated supported-citation metric) |
| **PRD ref** | QA-4; Product PRD §7 "Say I don't know"; Product PRD §8 supported ≥ 90% |
| **Depends on** | AI-1 (test harness and fakes for `rag.go`; same file, so do it after AI-1 to avoid conflicts) |
| **Status** | Ready (start after AI-1 merges) |

## Goal
In `internal/query/rag.go`, when the sentence embedding call fails or the cited chunk has no embedding, the citation is
labelled `supported = true` with `score = 0.75`
("Fallback if embedding service unavailable"). QA-4 requires every citation to be *verified*. An unverified citation
labelled supported breaks the core trust promise, and it inflates `supported_citation_rate` in `make eval`. Fail closed:
an unverified citation is never labelled supported.

## Scope
- `internal/query/rag.go`: in the support-check `else` branch, set `supported = false` and `score = 0`, and log the failure once per request (no secrets or passage text in the log). `SupportThreshold` stays as it is.
- `internal/query/rag_test.go`: add cases using AI-1's fake embedder for (a) a sentence-embed error, (b) a chunk-embed error, and (c) an empty vector result. All give `Supported == false` and `Score == 0`.
- Out of scope: a third "unknown/unverified" state. That would add a field to the `/v1/ask` response (a shared HTTP contract), so it needs an ADR. If you think it is needed, add a request in Notes; pm-lead will fold it into ADR-0003. Frontend changes are also out of scope.

## Acceptance criteria
- [ ] AC1 — No code path in `rag.go` sets `supported = true` without a computed cosine similarity ≥ `SupportThreshold` (`grep -n "supported = true" internal/query/rag.go` returns nothing, or only that comparison).
- [ ] AC2 — The three new test cases pass with no network, DB, or embedder.
- [ ] AC3 — The `/v1/ask` JSON shape is unchanged (same fields and types).
- [ ] AC4 — `make eval` before/after numbers pasted, or "not run: infra unavailable" plus the exact command. A drop in the supported rate is expected only if embed failures occurred. Explain any change.

## Verification
```bash
go vet ./... && gofmt -l internal/query
go test -cover ./internal/query/...
make test-unit
make lint
make eval   # when infra is available
```

## Notes
- Specced by pm-lead in PM-1 from EVAL-1 finding (c). AI-1 locks existing behaviour with tests and allows only small bug fixes (≤ 20 lines). The AI-2 code change is about 3 lines, but it is a separate task because failing closed is a product-behaviour decision (QA-4 trust promise, affects the gated supported-citation metric), not a bug fix found while testing.
- Verified on main: `rag.go` lines ~146–154 (`score = 0.75 // Fallback if embedding service unavailable`, `supported = true`).
