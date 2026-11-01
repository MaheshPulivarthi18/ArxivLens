# services/AGENTS.md — Python area rules

Owners: `services/processor/` and `services/common/` → **backend-engineer**; `services/embedder/` → **ai-engineer**.
Read the root `AGENTS.md` first.

## Setup (repo root)
```bash
python3 -m venv .venv && source .venv/bin/activate
pip install -r requirements-m0.txt minio ulid-py fastapi uvicorn   # until OPS-H2 unifies deps
export PYTHONPATH=$PWD
```

## Commands
| Command | What |
|---|---|
| `pytest tests/test_m0.py` | Chunker, embedder, citation validator (no infra; downloads BGE once) |
| `python services/processor/worker.py` | Processor worker (`papers.downloaded` → `papers.chunked`) |
| `python services/embedder/worker.py` | Embedder worker (`papers.chunked` → embeddings) |
| `python services/embedder/api.py` | FastAPI on :8081 (`/internal/embed`, `/internal/rerank`, `/internal/ingest`) |

## Rules
- The processor writes chunks and the `processed_events` ledger row in **one transaction** (idempotency layer L2). Keep it that way.
- The embedder only embeds chunks missing a vector for the current model (layer L3). Never re-embed blindly.
- Chunk format is a contract with retrieval quality: `"{paper title} — {section title}\n\n{text}"`, 512 tokens, 75 overlap. Changing it requires an ADR plus a full `make eval` comparison.
- Queries get the BGE instruction prefix; passages don't. Don't "fix" this.
- Device selection order is MPS → CUDA → CPU. Code must run on CPU-only machines (CI is CPU-only).
