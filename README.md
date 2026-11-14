# ArxivLens

**Distributed arXiv paper ingestion and semantic-search pipeline with grounded, cited answers.**

ArxivLens crawls arXiv, downloads paper PDFs, parses them into section-aware chunks, embeds them with `BAAI/bge-base-en-v1.5`, and serves hybrid (vector + BM25) search and a citation-validated Q&A endpoint behind a React UI.

- 🧑‍💻 [CONTRIBUTING.md](CONTRIBUTING.md): setup, commands, code layout and rules for contributors
- 📘 [Product PRD](https://github.com/MaheshPulivarthi18/arxivlens-agents/blob/main/docs/PRD-Product.md) · [Technical PRD](https://github.com/MaheshPulivarthi18/arxivlens-agents/blob/main/docs/PRD-Technical.md) · [ADRs](https://github.com/MaheshPulivarthi18/arxivlens-agents/tree/main/docs/adr) (in arxivlens-agents)

---

## Architecture at a glance

```mermaid
flowchart LR
    subgraph Ingestion["Ingestion pipeline (event-driven)"]
        C[Go Crawler<br/>arxivlens crawl] -->|papers.discovered| K1[(Kafka)]
        K1 --> D[Go Downloader<br/>arxivlens download]
        D -->|PDF| M[(MinIO<br/>arxiv-papers)]
        D -->|papers.downloaded| K2[(Kafka)]
        K2 --> P[Python Processor<br/>PyMuPDF + chunker]
        P -->|papers.chunked| K3[(Kafka)]
        K3 --> E[Python Embedder<br/>BGE-base 768d]
    end

    A[arXiv API] --> C
    A --> D
    M --> P
    P --> PG[(PostgreSQL 16<br/>+ pgvector)]
    E --> PG

    subgraph Serving["Query serving"]
        UI[React UI<br/>Vite :5173] -->|/v1/*| Q[Go Query Service :8080]
        Q -->|embed / rerank| EA[Embedder API<br/>FastAPI :8081]
        Q -->|hybrid RRF SQL| PG
    end
```

Every Kafka stage retries 3× with exponential backoff, then routes to a `<topic>.dlq` topic carrying full diagnostics. You can inspect and replay those messages with `arxivlens dlq`.

---

## Tech stack

| Layer | Technology |
|---|---|
| Crawler / Downloader / Query API / CLI | Go 1.25, Cobra, franz-go, pgx, minio-go |
| Processing / Embedding | Python 3.11, PyMuPDF, sentence-transformers / transformers, PyTorch (MPS/CUDA/CPU), FastAPI |
| Messaging | Apache Kafka 3.7 (KRaft, 12 partitions per topic) |
| Storage | PostgreSQL 16 + pgvector (`halfvec(768)`), MinIO (S3-compatible) |
| Models | `BAAI/bge-base-en-v1.5` (embeddings), `BAAI/bge-reranker-base` (cross-encoder) |
| Frontend | React 19, Vite, Tailwind CSS, Radix UI |
| Tracing | OpenTelemetry W3C `traceparent` propagated through event envelopes |

---

## Repository layout

```
cmd/arxivlens/        Go CLI entrypoint (migrate, crawl, download, status, query, dlq)
internal/
  config/             Env-driven configuration
  crawler/            arXiv Atom API crawler → papers.discovered
  downloader/         PDF fetch → MinIO → papers.downloaded
  kafka/              Producer + consumer with retry/DLQ routing
  dlq/                DLQ list / inspect / replay
  db/                 PostgreSQL access (pgx)
  storage/            MinIO client
  query/              HTTP server, hybrid search, RAG + citation validation, on-demand ingest
  telemetry/          OpenTelemetry tracer + traceparent helpers
services/
  processor/          papers.downloaded → parse + chunk → papers.chunked
  embedder/           papers.chunked → embeddings (worker.py) + FastAPI embed/rerank/ingest API (api.py)
  common/             Shared Python config, DB, Kafka, storage, telemetry
shared/events/        Event envelope + topic contracts (Go and Python)
migrations/           SQL schema (pgvector, papers, sections, chunks, embeddings, ledger)
scripts/              Core ML primitives (m0_skeleton), batch ingest, retrieval eval, HNSW benchmark
tests/                Milestone test suites (M0–M6): unit, infra, pipeline, query, chaos, telemetry
frontend/             React UI
```

---

## Quick start

### Prerequisites
- Docker + Docker Compose
- Go 1.25+
- Python 3.11+
- Node.js 20+

### 1. Start the stack (one command)

```bash
make stack-up                 # infra + processor, embedder worker, embedder API (:8081) in Docker; waits until healthy
```

`make stack-up` runs `docker compose --profile workers up -d --build` (CPU torch; models download once into the
`hf_cache` volume). Plain `docker compose up -d` (`make infra-up`) still starts **only** Postgres+pgvector, Kafka
(+ topics/DLQs) and MinIO (+ bucket). Stop everything with `make stack-down`.

> There is no `.dockerignore` yet: run `make stack-up` from a clean checkout, or move `.venv/` and `node_modules/` out
> of the repo root, or they are sent to Docker as build context.

| Service | URL |
|---|---|
| PostgreSQL | `localhost:5432` (postgres / postgres, db `arxivlens`) |
| Kafka (host) | `localhost:29092` |
| MinIO API / Console | `localhost:9000` / http://localhost:9001 (minioadmin / minioadmin) |

### 2. Build the Go CLI and migrate

```bash
go build -o bin/arxivlens ./cmd/arxivlens
./bin/arxivlens migrate
```

### 3. Run the Go services and crawl (host, separate terminals)

```bash
./bin/arxivlens download                      # Go downloader worker
./bin/arxivlens query --port 8080             # Go query service on :8080 (uses the embedder API on :8081)
./bin/arxivlens crawl --query "cat:cs.LG" --limit 20   # kick off discovery
```

Check one paper:

```bash
./bin/arxivlens status --arxiv-id 2307.08691
```

### 4. Run the UI

```bash
cd frontend && npm install && npm run dev     # UI on http://localhost:5173 (proxies /v1 → :8080)
```

### Option: Python workers on the host (Apple-silicon MPS acceleration)

Docker on macOS has no MPS, so for GPU-speed embedding on a Mac start infra only and run the three Python processes
on the host instead of `make stack-up`:

```bash
make infra-up
python3 -m venv .venv && source .venv/bin/activate && make setup-py && export PYTHONPATH=$PWD
python services/processor/worker.py           # Python processor worker
python services/embedder/worker.py            # Python embedder worker
python services/embedder/api.py               # Embed/rerank/ingest API on :8081
```

CUDA in compose is untested: `TORCH_INDEX_URL=https://download.pytorch.org/whl/cu121 make stack-up` plus a
`deploy.resources.reservations.devices` override for the GPU. CPU is the supported path.

---

## HTTP API (Query Service, `:8080`)

| Method | Path | Description |
|---|---|---|
| `GET` | `/healthz`, `/readyz` | Liveness / readiness |
| `GET` | `/v1/stats` | Corpus counts (papers, chunks, embedded) |
| `POST` | `/v1/search` | Hybrid search: `{query, categories?, limit?, rerank?}` → `{query, count, hits: [{chunk_id, arxiv_id, version, title, section, content, rrf_score, rerank_score?, page?}]}`. `version` is the arXiv version recorded for the chunk's paper; `page` is omitted until chunk pages are recorded reliably (ADR-0014) |
| `POST` | `/v1/ask` | Cited answer: `{question, categories?, max_citations?}` → `{answer, citations, trace_id, refusal_reason?}`. When no citation is supported, the answer is withheld, `refusal_reason` is `all_citations_unsupported` or `verification_unavailable`, and `citations` lists every relevant passage as evidence (ADR-0003) |
| `POST` | `/v1/papers/ingest` | On-demand ingest by arXiv ID or URL |
| `POST` | `/v1/papers/discover` | Find the best-matching arXiv paper for a query and ingest it |
| `GET` | `/v1/papers/{arxiv_id}` | Paper pipeline status |

```bash
curl -s localhost:8080/v1/ask -H 'content-type: application/json' \
  -d '{"question":"How does FlashAttention-2 improve GPU utilization?"}' | jq
```

---

## CLI reference

```
arxivlens migrate  [--dir migrations] [--file <one .up.sql>]
arxivlens crawl    [--query "cat:cs.AI OR cat:cs.LG"] [--limit 5] [--rate-limit 3s]
arxivlens download [--group downloader-v1]
arxivlens status   --arxiv-id <id>
arxivlens query    [--port 8080] [--embedder-url http://localhost:8081] [--model BAAI/bge-base-en-v1.5]
arxivlens dlq list    [--topic papers.downloaded.dlq] [--limit 50]
arxivlens dlq inspect [--topic ...] [--key <arxiv_id>]
arxivlens dlq replay  [--topic ...] [--error-type PdfParseError] [--limit 100] [--dry-run]
```

---

## Configuration

All services read the same environment variables (defaults target the local Docker stack):

| Variable | Default |
|---|---|
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/arxivlens?sslmode=disable` |
| `KAFKA_BROKERS` | `localhost:29092` |
| `MINIO_ENDPOINT` | `localhost:9000` |
| `MINIO_ACCESS_KEY` / `MINIO_SECRET_KEY` | `minioadmin` / `minioadmin` |
| `MINIO_BUCKET` | `arxiv-papers` |
| `MINIO_USE_SSL` (Go) / `MINIO_SECURE` (Python) | `false` |
| `EMBEDDING_MODEL` | `BAAI/bge-base-en-v1.5` |
| `INGEST_TIMEOUT` (Go query service) | `5m`: limit for the synchronous chunk + embed call of `POST /v1/papers/ingest` (Go duration) |
| `EMBEDDER_TORCH_THREADS` (embedder API) | Cores available to the process: CPU threads for torch (`torch.set_num_threads`). Invalid or non-positive values fall back to the default. Logged at startup as `torch threads: A -> B` |

> ⚠️ The default credentials are for local development only. Override them with your own values (e.g. a git-ignored `.env`) before deploying anywhere.

---

## Testing, evaluation and benchmarks

```bash
pytest tests/test_m0.py                    # Unit: chunker, embedder, citation validator (no infra)
pytest tests/test_evals_data.py            # Unit: eval datasets — schema, sizes, corpus membership, unique IDs (no infra)
make test-full                             # Full suite M0–M6 (host venv, make setup-py): needs make stack-up, make migrate, download + query running
make eval                                  # Quality gate vs Product PRD §8 (scripts/eval_gate.py); see evals/README.md
python scripts/evaluate_retrieval.py       # paper-level Recall@5 / MRR / NDCG@5 (+ chunk precision@5) across BM25, vector, RRF, RRF+rerank
python scripts/evaluate_answers.py         # /v1/ask: supported citations, refusals, p50/p95 latency
python scripts/benchmark_hnsw.py           # pgvector HNSW ef_search sweep (p50/p95/p99, QPS)
```

| Suite | Covers |
|---|---|
| M0 | Section-prefixed token chunking, 768-d normalized embeddings, citation validation |
| M1 | pgvector + schema, Kafka topics/partitions, MinIO bucket |
| M2 | Sections/chunks persisted, embeddings + cosine ops |
| M3 | Health, hybrid search + rerank, grounded `/v1/ask`, early refusal |
| M4 | Chaos: duplicate-event idempotency, corrupt-PDF → DLQ with diagnostics |
| M5/M6 | `traceparent` propagation, ranking metrics, HNSW index |

The `Full stack` workflow (`.github/workflows/stack.yml`) does all of this on a GitHub runner: `make stack-up`,
`make migrate`, starts `arxivlens download` and `arxivlens query` on the host, crawls 2 papers to `embedded`,
ingests the `2608.20331` fixture and runs `make test-full`. Known failure F1: the two M3 `/v1/ask` tests exceed their 10 s
timeout on CPU runners, so they are deselected there and run in a separate, non-blocking step. It runs on PRs that touch compose, Dockerfiles,
`requirements*.txt`, the Makefile, the workflow, Go code (`cmd/`, `internal/`, `go.mod`, `go.sum`) or `tests/`, and on demand (`workflow_dispatch`).
Every run, green or red, uploads a `stack-logs` artifact with `compose.log` and `embedder-timing.log`, plus `query.log` and `downloader.log` once the host services have started.

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). This repo contains only the runnable application, its tests and its CI.
Product requirements, design decisions (ADRs), tickets and QA evidence live in
[arxivlens-agents](https://github.com/MaheshPulivarthi18/arxivlens-agents), the workspace of the AI agent team that builds ArxivLens.

---

## Known limitations

- `/v1/ask` builds its answer **from a template** over the top reranked chunks. It does not call an LLM yet, though the citation validation pipeline is in place for when one is added.
- The Go downloader and query service are not in `docker-compose.yml`; run them on the host. The Python workers in compose run on CPU (run them on the host for Apple-silicon MPS).
- Tracing propagates `traceparent` but does not export spans to a collector.
