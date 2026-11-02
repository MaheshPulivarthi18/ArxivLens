# Contributing to ArxivLens

This repo contains only the runnable application: Go services and CLI, Python ML services, the React UI, SQL
migrations, tests, eval datasets and CI. Planning, tickets and design docs are kept elsewhere; PR descriptions link
to them.

## Setup

Prerequisites: Docker + Compose, Go 1.25+, Python 3.11+, Node.js 20+.

```bash
docker compose up -d                       # Postgres+pgvector, Kafka (+topics), MinIO
make build-go && ./bin/arxivlens migrate   # CLI + schema
python3 -m venv .venv && source .venv/bin/activate && make setup-py && export PYTHONPATH=$PWD
make setup-fe                              # frontend deps (npm ci)
```

## Commands

| Command | What | Needs infra? |
|---|---|---|
| `make help` | List targets | — |
| `make test-unit` | `go vet` + Go tests + `pytest tests/test_m0.py` | No |
| `make lint` | `go vet`, `gofmt` check, frontend `oxlint` | No |
| `make build` | Go binary + frontend build | No |
| `make infra-up` / `make infra-down` | Postgres+pgvector, Kafka, MinIO | Docker |
| `make test-full` | All milestone suites M0–M6 | Docker + workers running |
| `make eval` | Retrieval/answer quality gate over `evals/` | Docker + indexed corpus |

Frontend (from `frontend/`): `npm run dev` (:5173, proxies `/v1` → :8080), `npm run lint`, `npm run build`.
Python services: `python services/processor/worker.py`, `python services/embedder/worker.py`, `python services/embedder/api.py` (:8081).

## Layout

```
cmd/arxivlens/   Go CLI (migrate, crawl, download, status, query, dlq)
internal/        Go: crawler, downloader, kafka (retry/DLQ), dlq, db, storage, query (HTTP, hybrid search, RAG), telemetry
services/        Python: processor, embedder (worker + FastAPI), common
shared/events/   Event envelope contracts (Go + Python, kept in sync)
migrations/      SQL schema
scripts/         ML primitives, batch ingest, eval tooling, HNSW benchmark
evals/           Versioned eval datasets and thresholds (test data for `make eval`)
tests/           Milestone suites M0–M6 (pytest)
frontend/        React 19 + Vite UI
```

## Rules that keep the system correct

**Contracts** (change only with an agreed design decision, and update both sides in one PR):
- Event envelope and topics: `shared/events/events.go` and `shared/events/events.py`
- HTTP API: routes in `internal/query/server.go` and the API table in `README.md`
- DB schema: new numbered migrations may only *add* tables or indexes; never edit an applied migration
- Embedding model + dimension: `BAAI/bge-base-en-v1.5`, 768-d `halfvec`
- Quality gates: thresholds in `evals/thresholds.json` are never lowered to make a change pass

**Pipeline reliability** (keep `tests/test_m4_chaos.py` green):
- Natural-key upserts, commit-after-process, 3× retry with backoff then DLQ with full diagnostics, idempotent replays
- The processor writes chunks and the `processed_events` row in one transaction; the embedder only embeds chunks missing a vector for the current model
- Propagate `traceparent` from every incoming event to every outgoing one
- arXiv politeness: ≤ 1 request / 3 s with an identifying User-Agent

**ML**:
- Chunk format `"{paper title} — {section title}\n\n{text}"`, 512 tokens, 75 overlap. Queries get the BGE instruction prefix; passages don't
- Code must run on CPU-only machines (device order MPS → CUDA → CPU)
- Any LLM call reads its key from an env var, has a timeout, falls back to the templated answer or a refusal, and is stubbable in tests

**Frontend**:
- Consume only API fields that exist (`AskResponse` / `Citation` in `internal/query/rag.go`, README API table)
- Handle loading, empty, refusal, error and partial states; refusals are a normal state, not an error
- Every citation stays one click from its PDF; unsupported citations are visibly flagged

**Code style**: `gofmt`, table-driven Go tests that need no infra, small interfaces for testability; keep PRs small (< 400 changed lines).

## Pull requests

- One branch per change; PRs start as drafts while CI runs. Never push to `main`; a maintainer merges.
- CI must be green: Go (vet, gofmt, test, build), Python M0, frontend (lint, test, build).
- Branches named `agent/<role>/<task>` are produced by the project's AI agents. CI checks that each touches only its
  role's code area and adds no planning or review files (`.github/scripts/ownership-check.sh`).
