# Board

Last updated: 2026-11-02 by sprint cycle 2 (orchestrator)

WIP limit: 1 open PR per engineering agent. Order within a column = priority.

## Ready
| ID | Title | Owner | Pri | Depends on |
|---|---|---|---|---|
| [OPS-H1](tasks/OPS-H1.md) | Run processor + embedder workers in docker-compose; one-command full test | backend-engineer | P0 | — |
| [FE-1](tasks/FE-1.md) | Frontend test harness (Vitest) + tests for answer/citation states | frontend-engineer | P1 | — |
| [AI-1](tasks/AI-1.md) | Unit tests for `rag.go` filtering and citation validation (no infra) | ai-engineer | P1 | — |
| [OPS-H5](tasks/OPS-H5.md) | Migration for HNSW index + `migrate` applies all migrations (spec in PR #3) | backend-engineer | P1 | — (soft: OPS-H1) |
| [OPS-H2](tasks/OPS-H2.md) | Unify Python dependencies into one `requirements.txt` | backend-engineer | P1 | OPS-H1 |
| [ING-6](tasks/ING-6.md) | Scheduled crawler with per-category `since` watermark | backend-engineer | P1 | OPS-H3 |
| [FE-2](tasks/FE-2.md) | "Export all citations" (.bib) for an answer | frontend-engineer | P2 | FE-1 |
| [OPS-H4](tasks/OPS-H4.md) | `.gitignore` evals/results + run eval data tests in `make test-unit` (spec in PR #3) | backend-engineer | P1 | EVAL-1 |
| [AI-2](tasks/AI-2.md) | Fail closed when support-check embedding fails in `rag.go` (spec in PR #3) | ai-engineer | P1 | AI-1 |
| [AI-3](tasks/AI-3.md) | Batch embedding in RAG validation (spec in PR #3) | ai-engineer | P1 | AI-1, AI-2 |
| [EVAL-2](tasks/EVAL-2.md) | Cross-encoder rerank in eval + `/v1/search` p95 gate (spec in PR #3; **needs re-scoping**: EVAL-1 rework now calls `/internal/rerank`) | ai-engineer | P2 | EVAL-1 |

## In progress
| ID | Title | Owner | Branch |
|---|---|---|---|

## In QA
| ID | Title | Owner | PR |
|---|---|---|---|

## PM review
| ID | Title | Owner | PR |
|---|---|---|---|

## Ready to merge (raised for Mahesh)
| ID | Title | Owner | PR | Verdicts |
|---|---|---|---|---|
| [OPS-H3](tasks/OPS-H3.md) | Baseline Go unit tests (arXiv ID, Atom parsing, retry backoff) | backend-engineer | [#6](https://github.com/MaheshPulivarthi18/ArxivLens/pull/6) | QA PASS r1 · PM APPROVE (KAN-15) |
| [PM-1](tasks/PM-1.md) | Reconcile PRD status columns + acceptance criteria for all ⏳ rows | pm-lead | [#3](https://github.com/MaheshPulivarthi18/ArxivLens/pull/3) | QA FAIL r1 → PASS r2 · PM APPROVE (KAN-21). Merge after #2 |

## Blocked
| ID | Title | Owner | Waiting on |
|---|---|---|---|
| [EVAL-1](tasks/EVAL-1.md) | Versioned golden eval sets + `make eval` quality gate — PR [#2](https://github.com/MaheshPulivarthi18/ArxivLens/pull/2) | ai-engineer | Human review → rework f750bb9 → QA r2 FAIL: ADR-0006 (Recall@5 definition) + decision whether `/v1/ask` should sort by rerank score |
| [QA-8](tasks/QA-8.md) | LLM answer synthesis behind citation validation | ai-engineer | ADR-0001, ADR-0002 (human decision), EVAL-1, AI-1 |
| ADR-0006..0009 | Recall@5 definition · v2 multi-user scope · HNSW at scale · long-context embedder (drafted in PR #3) | Mahesh | human decision (block no Ready task) |

## Backlog (not yet specced — pm-lead grooms these)
- QA-9 Streaming answers (frontend + ai) — after QA-8
- OTLP span export to Jaeger/Tempo (backend) — Technical PRD §11 P1
- Prometheus metrics: consumer lag, DLQ depth, stage latency (backend) + OPS-6 dashboard (frontend)
- Auth + rate limiting on query API (backend) — before any shared deployment
- On-demand ingest via Kafka (backend) — depends on ADR-0004

## Environment notes (cycles 2026-11-01, 2026-11-02)
Agent sandbox egress blocked the Go 1.25 toolchain/modules, npm, Docker Hub and Hugging Face. Go, frontend and stack tasks (AI-1, OPS-H1/H2/H3/H5, ING-6, FE-1/2) were deferred, not blocked — they stay Ready. 2026-11-02: same limits (local Go 1.24.7); OPS-H3 was done anyway because CI verifies Go tests. OPS-H1 needs Docker, FE-1 needs npm to regenerate `package-lock.json`.

## Done
| ID | Title | PR |
|---|---|---|
