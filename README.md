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
- PostgreSQL 16 + **pgvector ≥ 0.8.0** (search uses `hnsw.iterative_scan`); `docker-compose.yml` pins an image with 0.8.7, so this matters only for your own database
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

The theme button in the navbar switches between Light, Dark and System (follows your OS); the choice is stored in your
browser (`localStorage`), and the first visit follows your OS setting (System).

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
| `POST` | `/v1/search` | Hybrid search: `{query, categories?, limit?, rerank?}` → `{query, count, hits: [{chunk_id, arxiv_id, version, title, section, content, rrf_score, rerank_score?, page?}]}`. `version` is the arXiv version recorded for the chunk's paper; `page` is the page the chunk's text starts on, when recorded; omitted otherwise (ADR-0014) |
| `POST` | `/v1/ask` | Cited answer: `{question, categories?, max_citations?, quick?}` → `{answer, citations: [{marker, arxiv_id, title, chunk_id, section, content, pdf_url, score, supported, paper_version, page?, quote?, status}], trace_id, refusal_reason?, answer_kind?, synthesis_available?, claims?: [{text, status, markers}], prompt_version?}`. Every cited claim is verified against its passage: a verbatim quote by exact match, anything else by NLI entailment (`/internal/verify`); an LLM claim with no verified citation is dropped, and a kept LLM claim shows only its verified citations (ADR-0019). Citation `status` (ADR-0014) is `supported`, `unsupported` (checked and failed) or `unverified` (not checked: the verifier was unavailable, or no statement cited this passage); `supported` is `status == "supported"`. `paper_version` is the indexed arXiv version (0 = unknown) and `pdf_url` opens that version (`…/pdf/<id>v<n>.pdf`; unversioned when 0); `page` is the page the chunk starts on, when recorded; `quote` (supported citations only) occurs in `content` after whitespace/inline-reference normalisation. `answer_kind` is `quoted` (sentences quoted from the passages) or `synthesized` (LLM claims); `"quick": true` skips the LLM and returns the quoted answer, with `synthesis_available: true` when a written answer can be requested without `quick`; `prompt_version` (e.g. `synthesis-v1`) is set only on `synthesized` answers (`quick`, `answer_kind`, `synthesis_available`: ADR-0018, Proposed). `claims` lists the answer's statements in order, each with its in-range `markers` (`[]` for an uncited sentence) and a `status`: `supported` if any of its citations is, else `unverified` if any is (or it has none), else `unsupported`. `citations` lists each claim's citations in claim order and, within a claim, in `markers` order, so claim *i*'s citations are `citations[o:o+len(markers)]` with `o` the total `markers` length of the claims before it. When nothing is supported, the answer is withheld, `refusal_reason` is `all_citations_unsupported`, `verification_unavailable` or (LLM only) `insufficient_evidence` (ADR-0018, Proposed), `citations` lists the relevant passages as evidence (ADR-0003), and `claims`, `answer_kind` and `prompt_version` are absent. When no passage is relevant, the answer says so with `citations: []` and no `refusal_reason` |
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
arxivlens crawl    [--query "cat:cs.AI OR cat:cs.LG"] [--limit 5] [--rate-limit 3s]   # --rate-limit below 3s is rejected
arxivlens crawl-loop [--categories cs.AI,cs.LG] [--interval 6h] [--limit-per-run 100] [--max-scan 2000] [--once]
arxivlens download [--group downloader-v1]
arxivlens status   --arxiv-id <id>
arxivlens query    [--port 8080] [--embedder-url http://localhost:8081] [--model BAAI/bge-base-en-v1.5]
arxivlens dlq list    [--topic papers.downloaded.dlq] [--partition P] [--from P/O] [--page-size 50] [--all]
arxivlens dlq inspect [--topic ...] [--key <arxiv_id>]
arxivlens dlq replay  [--topic ...] [--error-type PdfParseError] [--limit 100] [--dry-run]   # needs a migrated PostgreSQL
```

**Scheduled crawl.** `crawl-loop` runs a pass every `--interval` (at least `1m`, counted from the end of a pass; SIGINT/SIGTERM
stop it cleanly). Per category it lists arXiv newest-first down to the category's watermark (table `crawl_watermarks`), publishes
`papers.discovered` for at most `--limit-per-run` new papers, oldest first, and moves the watermark only past papers Kafka acked. So a
pass with nothing new emits nothing, and a crash or kill re-emits some papers (absorbed by the idempotent pipeline) but never skips one.
- **First run** of a category (no watermark) takes only its newest `--limit-per-run` papers; backfill older ones with `arxivlens crawl --query`.
- **One instance per database**: a second `crawl-loop` exits with `another crawl-loop is running` (PostgreSQL advisory lock), so arXiv
  sees one process at ≤ 1 request / 3 s. `--once` runs one pass and exits non-zero if any category failed.
- Each pass logs one line per category: `[CrawlLoop] cat=cs.LG pages=1 scanned=2 candidates=0 emitted=0 backlog=0 out_of_order=0 watermark=… (unchanged)`.
  `backlog=` > 0 means more new papers than `--limit-per-run`; they wait for the next pass. If it stays > 0, raise `--limit-per-run`
  (and `--max-scan`, which must be ≥ it). A backlog deeper than `--max-scan` fails the category loudly instead of skipping papers.
  `out_of_order=` counts feed entries newer than the one before them (arXiv's order is assumed newest-first; this checks it).
- **Reset a category** (the next pass is a first run again): `DELETE FROM crawl_watermarks WHERE category = 'cs.LG';`
- **Known gap**: a paper arXiv announces late (e.g. after moderation) with a `published` time already below the watermark is never
  picked up by `crawl-loop`; fetch it with `arxivlens crawl` or on-demand ingest. A lookback window is planned (ING-6e).

`dlq list` and `dlq inspect` read up to the high watermarks taken when the run starts (no consumer group, nothing committed).
Each record line starts with `P/O` (partition/offset); an undecodable record prints `P/O  UNDECODABLE (n bytes)`. `dlq list`
prints `--page-size` records (default 50; `--limit` is an alias) and ends with
`next page: arxivlens dlq list --topic T [--partition P] [--page-size N] --from P/O` (the optional flags only when given, so the line
can be pasted as-is) or `end of topic (high watermarks at start: 0=H0 1=H1 ...; N records)`, where N is the number of records listed in this run.
`--all` prints every page in one run. `inspect` scans the whole topic and stops at the first `--key` match. Expired offsets
(`WARNING partition P: offsets a..b expired`) go to stderr. A missing topic, a poll error or a stalled partition exits non-zero.
`inspect` on an empty topic prints `No DLQ events in topic: <T>`. Every error is printed once, as `Error: ...`.

**DLQ replay.** `dlq replay` republishes the DLQ's unresolved records, in partition then offset order, up to the high watermarks
taken at the start, with `attempt` reset to 0. It keeps its progress in PostgreSQL (`dlq_replay_checkpoints`, `dlq_replay_log`,
migration `000004`), so it needs `DATABASE_URL` and `arxivlens migrate`; without them it fails before reading Kafka.
- **Resumable:** repeated `--limit N` runs work through the whole DLQ; each record is resolved once (replayed, or
  `unreplayable` when there is nothing valid to republish). A record is marked resolved only after the broker acked its publish,
  so a crash re-publishes at most that record, with the same `event_id` (the consumers' idempotency absorbs it).
- **Filters:** with `--error-type X`, records of other types stay unresolved and the next run without the filter picks them up.
- **Run `--dry-run` first after deploy:** the first run has no checkpoint and replays everything still in retention. A dry run
  lists each would-be `P/O` and `event_id` under `DRY RUN`, writes nothing and publishes nothing.
- **One replay per DLQ at a time:** a real run holds a PostgreSQL session advisory lock for its DLQ topic. A second run of the same
  DLQ exits 1 with `another 'dlq replay' holds the lock for <topic> (pid P on <addr>, since <time>)` and publishes nothing; other DLQs
  replay in parallel, and `--dry-run` takes no lock. A crashed or killed holder releases the lock with its session. A **hung** holder keeps it:
  find it with `SELECT a.pid, a.client_addr, a.backend_start, a.state FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid WHERE l.locktype = 'advisory';`
  and release it with `SELECT pg_terminate_backend(<pid>);`. SIGINT/SIGTERM stop a run cleanly and release the lock.
- **Event IDs:** a replayed full envelope keeps its `event_id`. A raw payload (not a full envelope) is wrapped in a new envelope whose
  `event_id` is a ULID derived from the DLQ record's (topic, partition, offset), so every replay of it has the same ID. Its time part (and
  `occurred_at`) is the DLQ record's Kafka timestamp, not the original event's.
- **Reset a DLQ** (replay everything again): `DELETE FROM dlq_replay_log WHERE dlq_topic = $1; DELETE FROM dlq_replay_checkpoints WHERE dlq_topic = $1;`

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
| `LLM_MODEL` (query service) | Enables LLM answer synthesis (QA-8) with this model; unset: extractive answers (quoted sentences). Also `LLM_BASE_URL` (default Ollama `http://localhost:11434/v1`, any OpenAI-compatible API), `LLM_API_KEY`, `LLM_TIMEOUT` (default 30s), `LLM_MAX_TOKENS` (default 256). Answers come in two stages: `/v1/ask` with `"quick": true` returns the quoted answer in about the time of a search (the UI shows it at once), and the request without `quick` returns the written answer, whose every claim is verified against its cited passage. On any LLM failure, or when no written claim verifies, the answer falls back to the quoted one; `answer_kind` (`quoted`/`synthesized`) says which you got |
| `ARXIVLENS_NLI_MODEL` (embedder API) | NLI model for citation verification (default `cross-encoder/nli-deberta-v3-small`) |
| `ARXIV_API_URL`, `ARXIV_PDF_BASE` | Redirect arXiv fetches (CI points them at `scripts/arxiv_replay.py`); stored and displayed URLs stay canonical |
| `EMBEDDER_TORCH_THREADS` (embedder API) | Cores available to the process: CPU threads for torch (`torch.set_num_threads`). Invalid or non-positive values fall back to the default. Logged at startup as `torch threads: A -> B` |
| `EMBEDDER_RERANK_FP16` (embedder API) | On: the reranker runs in fp16 on MPS/CUDA (same eval ranking, about 3x faster on an M-series Mac; ADR-0020) and fp32 on CPU. `0`/`false`/`no`/`off` forces fp32. `GET /healthz` reports `reranker_dtype` |

> ⚠️ The default credentials are for local development only. Override them with your own values (e.g. a git-ignored `.env`) before deploying anywhere.

---

## Testing, evaluation and benchmarks

```bash
pytest tests/test_m0.py                    # Unit: chunker, embedder, citation validator (no infra)
pytest tests/test_evals_data.py            # Unit: eval datasets — schema, sizes, corpus membership, unique IDs (no infra)
make test-full                             # Full suite M0–M6 (host venv, make setup-py): needs make stack-up, make migrate, download + query running
make eval                                  # Quality gate vs Product PRD §8 (scripts/eval_gate.py); see evals/README.md
gh workflow run eval.yml                   # CI eval (also nightly, or Actions → Eval → Run workflow): gate table in the job log, results JSON in the run artifact, baseline in evals/BASELINE.md
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

- Without `LLM_MODEL`, `/v1/ask` answers by **quoting** the most relevant sentence of each top passage (verified by exact match). With an LLM, claims are verified by a small NLI model, which entailed 92% of verbatim sentences in a probe: expect some true paraphrases to be dropped.
- The Go downloader and query service are not in `docker-compose.yml`; run them on the host. The Python workers in compose run on CPU (run them on the host for Apple-silicon MPS).
- Tracing propagates `traceparent` but does not export spans to a collector.
