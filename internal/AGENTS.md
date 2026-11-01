# internal/AGENTS.md — Go area rules

Owner: **backend-engineer**, except `internal/query/rag.go` (+ `rag_test.go`), which is owned by **ai-engineer**.
Read the root `AGENTS.md` first.

## Commands (repo root)
| Command | What |
|---|---|
| `go build ./...` | Compile everything |
| `go vet ./...` | Static checks |
| `go test ./...` | Unit tests (no infra) |
| `gofmt -l .` | Must print nothing |
| `go build -o bin/arxivlens ./cmd/arxivlens` | CLI binary |

## Map
- `config/` env config · `crawler/` arXiv Atom → `papers.discovered` · `downloader/` PDF → MinIO → `papers.downloaded`
- `kafka/` producer + consumer with 3× backoff retry and DLQ routing · `dlq/` list/inspect/replay
- `db/` pgx access · `storage/` MinIO · `telemetry/` traceparent helpers
- `query/` HTTP server (`server.go`), hybrid RRF search (`search.go`), RAG + citation validation (`rag.go`), embedder client (`client.go`), on-demand ingest (`ingester.go`)

## Rules
- Reliability invariants are sacred (Technical PRD §8): natural-key upserts, commit-after-process,
  DLQ with full diagnostics, idempotent replays. Any change to `kafka/` or `dlq/` must keep `tests/test_m4_chaos.py` green.
- Respect arXiv politeness: ≤ 1 request / 3 s, identifying User-Agent. Never lower the rate limit.
- Propagate `traceparent` from every incoming event to every outgoing event.
- New Go code ships with table-driven unit tests that run without infra (fake or interface the dependencies).
- Event envelope changes must be made in `shared/events/events.go` **and** `events.py` in the same PR, behind an Accepted ADR.
