# QA report — OPS-H3 · PR #6 · round 1

**Verdict:** PASS
**Reviewed commit:** `af0461d79d0ba1b6fdad2773706cbb1d27d499e2` · **Date:** 2026-11-02
**Ticket:** `agents/tasks/OPS-H3.md` (spec as corrected in d173713) · **PRD:** ING-1, ING-4, ING-7, OPS-1

## Test results
| Check | Source | Result |
|---|---|---|
| Go (vet, gofmt, test, build) | CI run 41369125729, job 125035567407 | pass. Go 1.25.0; `ok` for `internal/crawler`, `internal/kafka` and `internal/query` (`evidence/01`) |
| Python M0 unit tests | CI | pass (`evidence/00`) |
| Frontend (lint, test, build) | CI | pass (`evidence/00`) |
| Agent guardrails | CI | pass (`evidence/00`) |
| Ticket verification commands | local, partly run | `gofmt -l` on all tracked Go files gives no output, so it is clean (`evidence/07`). `go vet` / `go test`: **not run locally**, because the sandbox has Go 1.24.7, go.mod needs 1.25.0 and the toolchain download is blocked (`evidence/07`) |
| `tests/test_m2_pipeline.py` (AC5) | not run (no Docker/Kafka/Postgres in sandbox) | — |
| `make eval` | not applicable (no retrieval or answering change) | — |

Note: CI runs `go test ./...` (Makefile `test-go`) **without `-v` or `-cover`**. Individual test names and PASS lines do not appear in the job log; only package-level `ok` lines do. Before this PR, `origin/main` had no `_test.go` files, and the PR adds exactly one test file to each of those three packages (`evidence/02`). So each `ok` line comes only from the new tests. I checked test content by reading the files at the head commit (`evidence/03`–`05`).

## Acceptance criteria
| AC | Met? | Evidence |
|---|---|---|
| AC1: `go test ./...` passes offline in CI | ✅ | `evidence/01-ac1-ci-go-test`: the `go test ./...` log lines show `ok arxivlens/internal/crawler 0.005s`, `ok …/kafka 0.004s` and `ok …/query 0.005s` on Go 1.25.0. `evidence/02`: these are the only test files. Run: https://github.com/MaheshPulivarthi18/ArxivLens/actions/runs/41369125729/job/125035567407 |
| AC2: ≥ 10 `ExtractCleanArxivID` cases, ≥ 3 invalid, including `cs/0601001` | ✅ | `evidence/03-ac2-extract-id-tests`: 14 cases and 4 `wantErr: true` (empty, whitespace-only, free text, `cs/0601001`). The expected values match the regex `\d{4}\.\d{4,5}` in `internal/query/ingester.go:58-66`. The 4- and 5-digit, `vN`, `abs/`, `pdf/`, `.pdf` and whitespace cases are all present. Test: `TestExtractCleanArxivID` (`internal/query/ingester_test.go`) |
| AC3: `parseAtomFeed` exists, `Crawl()` calls it, and the fixture test checks the ID/version split plus authors, categories and published date | ✅ | `evidence/04-ac3-parse-atom-feed`: the function is defined at `internal/crawler/crawler.go:191` and called from `Crawl()` at `crawler.go:125`. `TestParseAtomFeed` expects `2307.08691` v2 and `2401.12345` v1 (no suffix), and checks authors (the blank `<name>  </name>` is dropped and `" Alan Turing "` is trimmed), categories, the UTC published time, title/abstract whitespace folding and the PDF URL. The fixture is `internal/crawler/testdata/arxiv_feed.xml` with 2 entries. `TestParseAtomFeedEdgeCases` covers an empty feed and malformed XML |
| AC4: backoff is 200ms at attempt 1 and 400ms at attempt 2; loop range and sleep condition unchanged | ✅ | `evidence/05-ac4-backoff`: `TestRetryBackoff` asserts {1→200ms, 2→400ms}. The `consumer.go` diff replaces only the two-line sleep with `time.Sleep(retryBackoff(attempt))`. `for attempt := 1; attempt <= c.maxRetries` (l.92), `if attempt < c.maxRetries` (l.101) and `maxRetries: 3` (l.43) are untouched |
| AC5: extract-only refactor, behaviour unchanged; M2 suite (or "not run") | ✅ (M2 not run) | `evidence/06-ac5-moved-code`: a whitespace-normalised diff of the lines removed from and added to `crawler.go`. The only lines that differ are extraction glue: the call and error return, `len(papers)==0` in place of `len(feed.Entries)==0` (one record per entry, so the two are equivalent), reading locals from `paperRec`, and the `make`/`append`/`return`. The parse error is still wrapped as `failed to parse Atom XML: %w` and returned with `totalDiscovered`. The upsert gets a `*db.PaperRecord` with the same 8 fields and leaves the rest at zero, as before. Event payload keys and values are unchanged. `versionRegex` moved from a per-call local to a package var with the same pattern. I found no logic edits. `tests/test_m2_pipeline.py`: not run, because there is no infra in the sandbox |

## PRD fit
This is a test-only hardening task. It pins the current behaviour behind ING-1 and ING-7 (Atom mapping and version split into distinct `(arxiv_id, version)` records), ING-4 (ID/URL normalisation for on-demand ingest) and OPS-1 (3× in-process retry with exponential backoff). The PRD statuses are already ✅ and need no change. It does not change runtime behaviour, contracts, arXiv politeness or idempotency.

## Findings
1. [nit] The branch does not record the hand-off. The task spec still says `Status: Ready`, the AC checkboxes are not ticked with evidence (DoD 1), and `agents/board.md` and `agents/PROGRESS.md` are not updated on this branch (DoD 5). This is bookkeeping only; pm-lead or the developer can fix it when closing.
2. [nit] `TestRetrySleepSchedule` (`internal/kafka/consumer_test.go:26`) copies the loop shape instead of running `Consumer.Start`. A future edit to the real loop's range or condition would not make it fail. The "loop unchanged" part of AC4 is therefore proved by the diff, not by a test. The spec accepts this, since Kafka tests are out of scope.
3. [spec] The Verification section says `go test -cover ./...`, but CI runs `go test ./...` with no `-v` or `-cover`. As a result, CI evidence shows only package `ok` lines and no test names or coverage. Suggestion for pm-lead/backend: either have `make test-go` / CI use `go test -v -cover ./...`, or reword the spec so package-level `ok` counts as evidence.

## Missing tests
- `parseAtomFeed`: an entry whose `<published>` is not RFC3339 (current code silently gives a zero `time.Time`, which `Crawl` then formats as `0001-01-01T00:00:00Z`; worth pinning).
- `parseAtomFeed`: an `<id>` without `/abs/` (the fallback branch at `arxivIDWithVersion := rawID`), and a two-digit version such as `v12`.
- `ExtractCleanArxivID`: an input containing two IDs (the first match wins), and an old-style ID in URL form (`https://arxiv.org/abs/cs/0601001`, expecting an error).

## Evidence
- `evidence/00-ci-checks.png`: all 4 check runs on `af0461d` are completed/success, with job links.
- `evidence/01-ac1-ci-go-test.png`: lines from the Go CI job log for vet, test (package `ok` lines on Go 1.25.0) and build.
- `evidence/02-ac1-test-files.png`: `_test.go` files on `origin/main` (none) and at the PR head (3 new).
- `evidence/03-ac2-extract-id-tests.png`: the 14 table cases and the case counts, next to the function under test.
- `evidence/04-ac3-parse-atom-feed.png`: where `parseAtomFeed` is defined and called, the test's expected records, and the fixture's IDs, dates, authors and categories.
- `evidence/05-ac4-backoff.png`: the backoff test cases, the `consumer.go` diff and the untouched loop lines.
- `evidence/06-ac5-moved-code.png`: the diff of lines removed and added in `crawler.go`, showing that only extraction glue differs.
- `evidence/07-local-gofmt-gotest.png`: local `gofmt -l` (clean) and the local `go test` attempt, which is blocked by the Go 1.24.7 toolchain.

## Hand-off
PASS → pm-lead final review
