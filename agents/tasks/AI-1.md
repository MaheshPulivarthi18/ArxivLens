# AI-1: Unit tests for `rag.go` filtering and citation validation (no infra)

| | |
|---|---|
| **Owner** | ai-engineer |
| **Priority** | P1 (safety net before QA-8 rewrites synthesis) |
| **PRD ref** | QA-3, QA-4, QA-5, QA-6; Technical PRD §7.2 |
| **Depends on** | — |
| **Status** | Ready |

## Goal
`internal/query/rag.go` holds the product's core promise (grounded, validated citations; honest refusal) and has zero
Go tests. Lock its behaviour down before QA-8 replaces the template with an LLM.

## Scope
- `internal/query/rag.go`: introduce two small interfaces in this file, e.g. `searcher` (the `SearchEngine` methods `Ask` uses)
  and `embedder` (`EmbedTexts`). `NewRAGService` keeps its current signature (concrete types satisfy the interfaces), so callers don't change.
- New `internal/query/rag_test.go` with fakes. Table-driven tests for:
  - relevance cutoff (rerank < 0.45 dropped), auxiliary-section cutoff (appendix/supplementary/table < 0.70 dropped)
  - refusal when nothing passes
  - marker extraction: `[n]` with n outside `1..K` is discarded; multiple markers per sentence
  - support labelling at `SupportThreshold` boundary (0.49 → unsupported, 0.50 → supported)
  - `splitIntoSentences` edge cases (abbreviations like "e.g.", "et al.", decimals)
  - `cosineSimilarity` with zero vectors (no NaN)
- Out of scope: changing thresholds or behaviour. If a test exposes a bug, fix it only if the fix is ≤ 20 lines and note it; otherwise write the failing case as `t.Skip("bug: ...")` and request a follow-up task.

## Acceptance criteria
- [ ] AC1 — `go test ./internal/query/...` passes with no network, no DB, no embedder.
- [ ] AC2 — Every bullet above has at least one test case; coverage of `rag.go` ≥ 80% (`go test -cover`, paste number).
- [ ] AC3 — No public API or HTTP response shape changed.
- [ ] AC4 — Any bug found is listed in Notes with its test name.

## Verification
```bash
go vet ./... && gofmt -l internal/query
go test -cover ./internal/query/...
make test-unit
```

## Notes
