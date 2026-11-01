# OPS-H3: Baseline Go unit tests for crawler parsing, ID normalization, retry backoff

| | |
|---|---|
| **Owner** | backend-engineer |
| **Priority** | P1 |
| **PRD ref** | ING-1, ING-4, ING-7, OPS-1 |
| **Depends on** | — |
| **Status** | Ready |

## Goal
The Go codebase has no `_test.go` files, so CI can only check that it compiles. Add fast, infra-free tests around
the pure logic most likely to break.

## Scope
- `internal/query/ingester.go`: tests for `ExtractCleanArxivID` — bare IDs (new `2307.08691` and old `cs/0601001` style),
  versioned IDs (`v2`), `abs/` and `pdf/` URLs, `.pdf` suffix, whitespace, invalid input.
- `internal/crawler/crawler.go`: extract the Atom-feed → paper mapping into a pure function (e.g. `parseAtomFeed([]byte)`)
  if it isn't already, and test it against a small fixture in `internal/crawler/testdata/` (ID + version split, authors, categories, published date).
- `internal/kafka/consumer.go`: extract the backoff calculation into a tiny function and test 100ms × 2^attempt for attempts 0..2.
- Out of scope: tests that need Kafka, Postgres, MinIO or network.

## Acceptance criteria
- [ ] AC1 — `go test ./...` passes offline.
- [ ] AC2 — `ExtractCleanArxivID` has ≥ 10 table cases including at least 3 invalid inputs.
- [ ] AC3 — Atom fixture test asserts arXiv ID and version are split correctly (ING-7).
- [ ] AC4 — Behaviour unchanged: refactors are extract-function only; `tests/test_m2_pipeline.py` still passes when infra is available (or note "not run").

## Verification
```bash
go vet ./... && test -z "$(gofmt -l .)"
go test -cover ./...
```

## Notes
