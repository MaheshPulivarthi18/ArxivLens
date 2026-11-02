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
- `internal/query/ingester.go`: table tests for `ExtractCleanArxivID` that **pin current behaviour** (regex `\d{4}\.\d{4,5}`):
  bare new-style IDs (`2307.08691`, 4- and 5-digit suffixes), versioned IDs (`2307.08691v2` → `2307.08691`, version dropped),
  `abs/` and `pdf/` URLs, `.pdf` suffix, surrounding whitespace, and invalid inputs that return an error
  (empty string, free text, old-style `cs/0601001`). Old-style IDs are **not** supported today; do not add support here.
- `internal/crawler/crawler.go`: extract the inline Atom → paper mapping in `Crawl()` (XML unmarshal, `/abs/` split,
  `versionRegex` ID/version split, title/abstract whitespace folding, authors, categories, RFC3339 published date, PDF URL)
  into `parseAtomFeed(body []byte) ([]db.PaperRecord, error)`. `Crawl()` must call it in place of the inline code.
  Test it against a small fixture in `internal/crawler/testdata/` (≥ 2 entries, one with `v2`, one with no version suffix).
  Out of scope: the duplicate `atomFeed` structs in `internal/query/ingester.go`.
- `internal/kafka/consumer.go`: extract the sleep calculation into a tiny function (e.g. `retryBackoff(attempt int) time.Duration`
  returning `100ms × 2^attempt`). Keep the loop exactly as is (`attempt` starts at 1, sleeps only while `attempt < maxRetries`,
  `maxRetries = 3`). Test the delays the loop actually uses: attempt 1 → 200ms, attempt 2 → 400ms.
- Out of scope: tests that need Kafka, Postgres, MinIO or network; changing any runtime behaviour.

## Acceptance criteria
- [ ] AC1 — `go test ./...` passes offline **in CI** (GitHub Actions run link is the evidence; see Verification).
- [ ] AC2 — `ExtractCleanArxivID` has ≥ 10 table cases asserting current outputs, including ≥ 3 invalid inputs, one of which is old-style `cs/0601001` (expects error).
- [ ] AC3 — `parseAtomFeed` exists, `Crawl()` calls it, and the fixture test asserts arXiv ID and version are split correctly (`…v2` → version 2; no suffix → version 1) plus authors, categories and published date (ING-7).
- [ ] AC4 — Backoff test asserts attempt 1 → 200ms and attempt 2 → 400ms; the retry loop's attempt range and sleep condition are unchanged.
- [ ] AC5 — Behaviour unchanged: refactors are extract-function only (diff shows moved code, no logic edits); `tests/test_m2_pipeline.py` still passes when infra is available (or note "not run").

## Verification
```bash
go vet ./... && test -z "$(gofmt -l .)"
go test -cover ./...
```
`go.mod` requires Go 1.25.0. The sandbox has Go 1.24.7 and the toolchain download is blocked, so `go vet`/`go test`
may not run locally. In that case `gofmt -l .` is the only local check; AC1–AC4 are verified by the CI run on the PR. Paste the run link.

## Notes
- 2026-11-02 (pm-lead): spec fixed before build — pinned current behaviour for old-style IDs and backoff; named parseAtomFeed; Go tests verified in CI only.
- Old-style arXiv ID support (`cs/0601001`) → follow-up to spec (pm-lead).
- 2026-11-02 (backend-engineer): implemented on `agent/backend/OPS-H3`. Added `parseAtomFeed` + `versionRegex` (package var)
  in `crawler.go` (Crawl calls it), `retryBackoff` in `consumer.go`, tests `internal/{crawler,kafka,query}/*_test.go`,
  fixture `internal/crawler/testdata/arxiv_feed.xml`. Local: `gofmt -l internal/` clean; full `go test ./internal/...`
  could not run (Go 1.24.7, module deps not cached, proxy blocked). New tests pass under Go 1.24 in a stdlib-only scratch
  copy, and the real `crawler.go` passes `go vet` against stubbed db/kafka/events. `tests/test_m2_pipeline.py` not run (no infra).
  AC1 awaits the CI run on the PR.
